// Package gitwatch provides a simple tool to first clone a set of git
// repositories to a local directory and then periodically check them all for
// any updates.
package gitwatch

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/xerrors"
	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
	"gopkg.in/src-d/go-git.v4/plumbing/transport"
)

// Repository represents a Git repository address and branch name
type Repository struct {
	URL       string               // local or remote repository URL to watch
	Branch    string               // the name of the branch to use `master` being default
	Directory string               // the directory name to clone the repository to, relative from the session's directory
	Auth      transport.AuthMethod // authentication method for git operations

	fullPath string // the full path, computed at construction time
}

// Session represents a git watch session configuration
type Session struct {
	Repositories []Repository         // list of local or remote repository URLs to watch
	Interval     time.Duration        // the interval between remote checks
	Directory    string               // the directory to store repositories
	Auth         transport.AuthMethod // authentication method for git operations
	InitialEvent bool                 // if true, an event for each repo will be emitted upon construction
	InitialDone  chan struct{}        // if InitialEvent true, this is pushed to after initial setup done
	Events       chan Event           // when a change is detected, events are pushed here
	Errors       chan error           // when an error occurs, errors come here instead of halting the loop

	running  bool            // has the watcher started?
	newRepos chan Repository // new repositories to add at runtime

	ctx context.Context
	cf  context.CancelFunc
}

// Event represents an update detected on one of the watched repositories
type Event struct {
	URL       string
	Path      string
	Timestamp time.Time
	commit    object.Commit
}

// Commit returns the (immutable) commit associated with an event
func (e Event) Commit() object.Commit {
	return e.commit
}

// New constructs a new git watch session on the given repositories
// The `auth` parameter is the default authentication method. Elements of the
// `repos` list may specify their own authentication methods, which override
// this value when set.
func New(
	ctx context.Context,
	repos []Repository,
	interval time.Duration,
	dir string,
	auth transport.AuthMethod,
	initialEvent bool,
) (session *Session, err error) {
	r, err := hydrateRepos(dir, repos)
	if err != nil {
		return nil, err
	}

	ctx2, cf := context.WithCancel(ctx)

	session = &Session{
		Repositories: r,
		Interval:     interval,
		Directory:    dir,
		Auth:         auth,
		Events:       make(chan Event, len(repos)),
		Errors:       make(chan error, 16),
		InitialEvent: initialEvent,
		InitialDone:  make(chan struct{}, 1),

		ctx: ctx2,
		cf:  cf,
	}
	return
}

// Run begins the watcher and blocks until an error occurs
func (s *Session) Run() (err error) {
	return s.daemon()
}

// IsRunning returns true if `Run` has been called
func (s *Session) IsRunning() bool {
	return s.running
}

// Add will add a new repository to the list. Works even after the watcher
// daemon has already been started.
func (s *Session) Add(r Repository) (err error) {
	r, err = hydrate(s.Directory, r)
	if err != nil {
		return
	}
	if s.running {
		s.newRepos <- r
	} else {
		s.Repositories = append(s.Repositories, r)
	}
	return
}

// Close gracefully shuts down the git watcher
func (s *Session) Close() {
	s.cf()
	s.running = false
}

func (s *Session) daemon() (err error) {
	s.running = true
	t := time.NewTicker(s.Interval)

	// a function to select over the session's context and the ticker to check
	// repositories.
	f := func() (err error) {
		select {
		case <-s.ctx.Done():
			err = s.ctx.Err()
		case <-t.C:
			err = s.checkRepos(false)
			if err != nil {
				if xerrors.Is(err, io.EOF) {
					return nil
				}
				s.Errors <- err
				return nil
			}
		case r := <-s.newRepos:
			s.Repositories = append(s.Repositories, r)
		}
		return
	}

	// before starting the daemon process loop, perform an initial check against
	// all targets. If the targets do not exist, they will be cloned and events
	// will be emitted for them.
	err = s.checkRepos(s.InitialEvent)
	if err != nil {
		return
	}
	s.InitialDone <- struct{}{}

	for {
		err = f()
		if err != nil {
			return
		}
	}
}

// hydrateRepos fills in the full dir paths based on the watcher's root. If a
// repo specifies a custom path, that is used, otherwise it figures out the path
// from the URL.
func hydrateRepos(root string, in []Repository) (out []Repository, err error) {
	out = make([]Repository, len(in))
	for i, r := range in {
		out[i], err = hydrate(root, r)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func hydrate(root string, r Repository) (Repository, error) {
	var directory string
	if r.Directory == "" {
		d, err := GetRepoDirectory(r.URL)
		if err != nil {
			return r, errors.Wrapf(err, "failed to get path from repo url %s", r.URL)
		}
		directory = d
	} else {
		directory = r.Directory
	}
	r.fullPath = filepath.Join(root, directory)
	return r, nil
}

// checkRepos simply iterates all repositories and collects events from them, if
// there are any, they will be emitted to the Events channel concurrently.
func (s *Session) checkRepos(initial bool) (err error) {
	for _, repository := range s.Repositories {
		var event *Event
		event, err = s.checkRepo(repository, initial)
		if err != nil {
			return
		}

		if event != nil {
			go func() { s.Events <- *event }()
		}
	}
	return
}

// checkRepo checks a specific git repository that may or may not exist locally
// and if there are changes or the repository had to be cloned fresh (and
// InitialEvents is true) then an event is returned.
func (s *Session) checkRepo(repository Repository, initial bool) (event *Event, err error) {
	repo, err := git.PlainOpen(repository.fullPath)
	if err != nil {
		if err != git.ErrRepositoryNotExists {
			err = errors.Wrap(err, "failed to open local repo")
			return
		}

		repo, err = s.cloneRepo(repository)
		if err != nil {
			return
		}
	}

	// always generate an event for the initial check
	if initial {
		return GetEventFromRepo(repo)
	}

	// otherwise, check for new events - if there are any changes, `event` will
	// not be nil.
	evt, err := s.GetEventFromRepoChanges(repo, repository.Branch, repository.Auth)
	if err != nil {
		// fresh start if there was a failure
		if err := os.RemoveAll(repository.fullPath); err != nil {
			return nil, errors.Wrap(err, "failed to remove repository for re-clone")
		}

		repo, err = s.cloneRepo(repository)
		if err != nil {
			return nil, errors.Wrap(err, "failed to clone repository for re-clone")
		}
		return GetEventFromRepo(repo)
	}
	return evt, nil
}

// cloneRepo clones the specified repository to the session's cache.
func (s *Session) cloneRepo(repository Repository) (repo *git.Repository, err error) {
	var ref plumbing.ReferenceName
	if repository.Branch != "" {
		ref = plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", repository.Branch))
	}

	repo, err = git.PlainCloneContext(s.ctx, repository.fullPath, false, &git.CloneOptions{
		Auth:              s.chooseAuth(repository.Auth),
		URL:               repository.URL,
		ReferenceName:     ref,
		RecurseSubmodules: git.DefaultSubmoduleRecursionDepth,
	})
	if err != nil {
		err = errors.Wrap(err, "failed to clone initial copy of repository")
		return
	}
	return
}

// GetEventFromRepoChanges reads a locally cloned git repository an returns an
// event only if an attempted fetch resulted in new changes in the working tree.
func (s *Session) GetEventFromRepoChanges(repo *git.Repository, branch string, auth transport.AuthMethod) (event *Event, err error) {
	wt, err := repo.Worktree()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get worktree")
	}

	var ref plumbing.ReferenceName
	if branch != "" {
		ref = plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", branch))
	}

	err = wt.Pull(&git.PullOptions{
		Auth:              s.chooseAuth(auth),
		ReferenceName:     ref,
		RecurseSubmodules: git.DefaultSubmoduleRecursionDepth,
		Force:             true,
	})
	if err != nil {
		if err == git.NoErrAlreadyUpToDate {
			return nil, nil
		}
		return nil, errors.Wrap(err, "failed to pull local repo")
	}

	return GetEventFromRepo(repo)
}

// GetEventFromRepo reads a locally cloned git repository and returns an event
// based on the most recent commit.
func GetEventFromRepo(repo *git.Repository) (event *Event, err error) {
	wt, err := repo.Worktree()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get worktree")
	}
	remote, err := repo.Remote("origin")
	if err != nil {
		return
	}
	ref, err := repo.Head()
	if err != nil {
		return
	}
	c, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return
	}
	return &Event{
		URL:       remote.Config().URLs[0],
		Path:      wt.Filesystem.Root(),
		Timestamp: c.Author.When,
		commit:    *c,
	}, nil
}

// GetRepoDirectory the directory name for a repository.
func GetRepoDirectory(repo string) (string, error) {
	if strings.HasPrefix(repo, "http") {
		u, err := url.Parse(repo)
		if err != nil {
			return "", err
		}
		return filepath.Base(u.EscapedPath()), nil
	} else {
		path := strings.Split(repo, ":")
		i := 0
		if len(path) == 2 {
			i = 1
		}
		u, err := url.Parse(path[i])
		if err != nil {
			return "", err
		}
		return filepath.Base(u.Path), nil
	}
}

func (s *Session) chooseAuth(a transport.AuthMethod) transport.AuthMethod {
	if a != nil {
		return a
	}
	return s.Auth
}
