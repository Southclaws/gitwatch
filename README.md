# `gitwatch`

Periodically polls a set of targets for changes, if there are any an event is
emitted on a channel.

## Usage

```go
session, err := gitwatch.New(
    ctx,
    []string{"https://github.com/repo/a", "https://github.com/repo/b#branch"},
    time.Second,
    "./gitwatch-cache/",
    true,
)

go func() {
    for {
        select {
        case event := <-session.Events:
            fmt.Println("git event:", event)
        case err := <-session.Errors:
            fmt.Println("git error:", err)
        }
    }
}()

// blocks until failure
err = session.Run()
if err != nil {
    // process was terminated somehow, handle error
}
```

By design, once the watcher is up and running (post initial clone phase), errors
will not cause it to stop. Instead, errors are passed down the `Errors` channel
for the dependent package to handle. The error returned by `Run` will either be
`context.Cancelled` or any git errors raised during the initial cloning of all
targets.

There also exists a channel called `InitialDone` which is only ever pushed to
once, immediately after all initial targets have been cloned. It's a buffered
channel of size 1 so there's no explicit need to ever read from it but it can be
useful for sequencing things properly.

You can specify branches by appending the repository path with a `#` character
followed by the branch name. Thanks to @ADRFranklin for this feature!
