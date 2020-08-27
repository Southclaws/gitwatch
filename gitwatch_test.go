package gitwatch_test

import (
	"context"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Southclaws/gitwatch"
	"github.com/bmizerany/assert"
	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
)

var (
	gw      *gitwatch.Session
	ctx     context.Context
	cf      context.CancelFunc
	initial time.Time
)

func TestMain(m *testing.M) {
	var err error

	mockRepo("a")
	mockRepo("b")
	mockRepo("c")
	err = os.RemoveAll("./test/gitwatch.git")
	if err != nil {
		panic(err)
	}

	log.Println("creating global watcher")
	ctx, cf = context.WithCancel(context.Background())
	gw, err = gitwatch.New(
		ctx,
		[]gitwatch.Repository{
			{URL: "./test/local/a"},
			{URL: "./test/local/b"},
			{URL: "./test/local/c"},
			{URL: "https://github.com/Southclaws/gitwatch.git"},
		},
		time.Second,
		"./test/",
		nil,
		true,
	)
	if err != nil {
		panic(err)
	}

	go func() {
		log.Println("starting global watcher daemon")
		err2 := gw.Run()
		if err2 != nil && err2 != context.Canceled {
			log.Fatal(err2)
			return
		}
	}()

	go func() {
		log.Println("listening for errors")
		err2 := <-gw.Errors
		if err2 != nil {
			cf()
		}
	}()

	log.Println("waiting for events")

	// consume clone events
	log.Println("consumed initial event:", <-gw.Events)
	log.Println("consumed initial event:", <-gw.Events)
	log.Println("consumed initial event:", <-gw.Events)
	log.Println("consumed initial event:", <-gw.Events)

	<-gw.InitialDone

	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, os.Interrupt)
		buf := make([]byte, 1<<20)
		for {
			<-sigs
			stacklen := runtime.Stack(buf, true)
			log.Printf("\n%s\n", buf[:stacklen])
		}
	}()

	ret := m.Run()

	gw.Close()

	os.Exit(ret)
}

func assertEventsEqual(t *testing.T, a, b gitwatch.Event) {
	assert.Equal(t, a.URL, b.URL)
	assert.Equal(t, a.Path, b.Path)
	assert.T(t, a.Timestamp.Equal(b.Timestamp))
}

func consumeAndAssert(t *testing.T, events chan gitwatch.Event, expected gitwatch.Event) {
	assertEventsEqual(t, expected, <-events)
}

func TestMakeChange1(t *testing.T) {
	ts := mockRepoChange("a", "hello world!")
	consumeAndAssert(t, gw.Events, gitwatch.Event{
		URL:       "./test/local/a",
		Path:      fullPath("./test/a"),
		Timestamp: ts.Truncate(time.Second),
	})
}

func TestMakeChange2(t *testing.T) {
	tsa := mockRepoChange("a", "hello world!!")
	consumeAndAssert(t, gw.Events, gitwatch.Event{
		URL:       "./test/local/a",
		Path:      fullPath("./test/a"),
		Timestamp: tsa.Truncate(time.Second),
	})

	tsb := mockRepoChange("b", "hello earth")
	consumeAndAssert(t, gw.Events, gitwatch.Event{
		URL:       "./test/local/b",
		Path:      fullPath("./test/b"),
		Timestamp: tsb.Truncate(time.Second),
	})
}

func TestMakeChangeWithReset(t *testing.T) {
	if _, err := os.Create("./test/c/file2"); err != nil {
		panic(err)
	}
	tsa := mockRepoChange("c", "second file to cause dirty repo!!")
	consumeAndAssert(t, gw.Events, gitwatch.Event{
		URL:       "./test/local/c",
		Path:      fullPath("./test/c"),
		Timestamp: tsa.Truncate(time.Second),
	})
}

func mockRepo(name string) {
	dirPath := filepath.Join("./test/local/", name)
	err := os.RemoveAll(dirPath)
	if err != nil {
		panic(err)
	}
	err = os.RemoveAll(filepath.Join("./test", name))
	if err != nil {
		panic(err)
	}
	repo, err := git.PlainInit(dirPath, false)
	if err != nil {
		panic(err)
	}
	err = ioutil.WriteFile(filepath.Join(dirPath, "file"), []byte("hello world"), 0666)
	if err != nil {
		panic(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		panic(err)
	}
	_, err = wt.Add("file")
	if err != nil {
		panic(err)
	}
	_, err = wt.Commit("first", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "test",
			Email: "test@test.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		panic(err)
	}
}

func mockRepoChange(name, contents string) time.Time {
	dirPath := filepath.Join("./test/local/", name)
	repo, err := git.PlainOpen(dirPath)
	if err != nil {
		panic(err)
	}
	err = ioutil.WriteFile(filepath.Join(dirPath, "file"), []byte(contents), 0666)
	if err != nil {
		panic(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		panic(err)
	}
	_, err = wt.Add("file")
	if err != nil {
		panic(err)
	}
	ts := time.Now()
	_, err = wt.Commit("add: "+contents, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "test",
			Email: "test@test.com",
			When:  ts,
		},
	})
	if err != nil {
		panic(err)
	}
	log.Println("committed mock change", contents, "to", name)
	return ts
}

func fullPath(relative string) (result string) {
	result, err := filepath.Abs(relative)
	if err != nil {
		panic(err)
	}
	return
}

func TestGetRepoDirectory(t *testing.T) {
	type args struct {
		repo string
	}
	tests := []struct {
		name     string
		args     args
		wantPath string
		wantErr  bool
	}{
		{"https", args{"https://a.com/user/repo"}, "repo", false},
		{"https_long", args{"https://a.com/user/namespace/repo"}, "repo", false},
		{"ssh", args{"git@a.com:user/repo"}, "repo", false},
		{"ssh_short", args{"git@a.com:repo"}, "repo", false},
		{"ssh_long", args{"git@a.com:user/s/u/b/d/i/r/repo"}, "repo", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPath, err := gitwatch.GetRepoDirectory(tt.args.repo)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetRepoDirectory() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotPath != tt.wantPath {
				t.Errorf("GetRepoDirectory() = %v, want %v", gotPath, tt.wantPath)
			}
		})
	}
}
