package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultPartSize is the largest part the server takes (64 MiB).
	DefaultPartSize int64 = 64 << 20
	// DefaultParallel is how many files are uploaded at once.
	DefaultParallel = 4

	maxAttempts = 6
)

// File outcomes.
const (
	StatusUploaded = "uploaded"
	StatusSkipped  = "already uploaded"
	StatusEmpty    = "empty, not uploaded"
	StatusFailed   = "failed"
)

// Options configure one run.
type Options struct {
	Dir        string
	Snapshot   string // label or id; empty picks a label from the folder name and capture date
	CapturedOn string // YYYY-MM-DD, today when empty
	Scope      string
	Close      bool
	Parallel   int

	// Progress receives one calm line per event. It may be called from several goroutines, one at a time.
	Progress func(line string)

	// Tuning for tests; zero values mean the defaults.
	PartSize    int64
	RetryBase   time.Duration
	PollEvery   time.Duration
	PollTimeout time.Duration
	Now         func() time.Time
}

// FileResult is the outcome for one file.
type FileResult struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Status  string `json:"status"`
	Resumed bool   `json:"resumed,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Result is the outcome of a run.
type Result struct {
	Snapshot Snapshot     `json:"snapshot"`
	Files    []FileResult `json:"files"`
	Closed   bool         `json:"closed"`
	// CloseSkipped says why --close did nothing: some file did not complete.
	CloseSkipped string `json:"closeSkipped,omitempty"`
}

// Complete counts files that are on the server, and Failed those that are not.
func (r Result) Complete() int {
	return r.count(func(f FileResult) bool { return f.Status != StatusFailed })
}
func (r Result) Failed() int {
	return r.count(func(f FileResult) bool { return f.Status == StatusFailed })
}

func (r Result) count(match func(FileResult) bool) int {
	n := 0
	for _, f := range r.Files {
		if match(f) {
			n++
		}
	}
	return n
}

type runner struct {
	client *Client
	opts   Options
	state  *stateStore
	snap   Snapshot
	mu     sync.Mutex
}

func (o Options) partSize() int64 {
	if o.PartSize > 0 {
		return o.PartSize
	}
	return DefaultPartSize
}

func (o Options) retryBase() time.Duration {
	if o.RetryBase > 0 {
		return o.RetryBase
	}
	return time.Second
}

func (o Options) pollEvery() time.Duration {
	if o.PollEvery > 0 {
		return o.PollEvery
	}
	return time.Second
}

func (o Options) pollTimeout() time.Duration {
	if o.PollTimeout > 0 {
		return o.PollTimeout
	}
	return 15 * time.Minute
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (r *runner) say(format string, args ...any) {
	if r.opts.Progress == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opts.Progress(fmt.Sprintf(format, args...))
}

// Run uploads every file below opts.Dir into a snapshot and returns what happened per file.
// An error means the run could not start; per-file failures are in the result.
func Run(ctx context.Context, client *Client, opts Options) (Result, error) {
	info, err := os.Stat(opts.Dir)
	if err != nil || !info.IsDir() {
		return Result{}, fmt.Errorf("%s is not a folder", opts.Dir)
	}
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return Result{}, err
	}
	opts.Dir = dir
	if opts.CapturedOn == "" {
		opts.CapturedOn = opts.now().Format("2006-01-02")
	} else if _, err := time.Parse("2006-01-02", opts.CapturedOn); err != nil {
		return Result{}, errors.New("--capture-date must be a day like 2026-03-31")
	}
	files, err := walk(dir)
	if err != nil {
		return Result{}, fmt.Errorf("read the folder: %w", err)
	}
	if len(files) == 0 {
		return Result{}, fmt.Errorf("%s has no files to upload", dir)
	}

	r := &runner{client: client, opts: opts}
	if r.snap, err = r.resolveSnapshot(ctx); err != nil {
		return Result{}, err
	}
	r.state = loadState(dir, client.ProjectID, r.snap.ID)

	results := r.uploadAll(ctx, files)
	out := Result{Snapshot: r.snap, Files: results}
	if opts.Close {
		switch {
		case ctx.Err() != nil:
			out.CloseSkipped = "the upload was interrupted"
		case out.Failed() > 0:
			out.CloseSkipped = fmt.Sprintf("%d of %d files did not complete", out.Failed(), len(results))
		default:
			closed, err := client.CloseDrop(ctx, r.snap.ID)
			if err != nil {
				return out, fmt.Errorf("all files are uploaded, but closing the drop failed: %w", err)
			}
			out.Snapshot, out.Closed = closed, true
		}
	}
	return out, ctx.Err()
}

// resolveSnapshot finds the named open snapshot or opens one.
func (r *runner) resolveSnapshot(ctx context.Context) (Snapshot, error) {
	label := strings.TrimSpace(r.opts.Snapshot)
	if label == "" {
		label = filepath.Base(r.opts.Dir) + " " + r.opts.CapturedOn
	}
	snapshots, err := r.client.ListSnapshots(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if i := slices.IndexFunc(snapshots, func(s Snapshot) bool { return s.ID == label || s.Label == label }); i >= 0 {
		found := snapshots[i]
		if found.State != "open" {
			return Snapshot{}, fmt.Errorf("snapshot %q is %s and takes no more files", found.Label, found.State)
		}
		r.say("using snapshot %s", found.Label)
		return found, nil
	}
	opened, err := r.client.OpenSnapshot(ctx, label, r.opts.CapturedOn, r.opts.Scope)
	if err != nil {
		return Snapshot{}, fmt.Errorf("open snapshot %q: %w", label, err)
	}
	r.say("opened snapshot %s", opened.Label)
	return opened, nil
}

func (r *runner) uploadAll(ctx context.Context, files []localFile) []FileResult {
	parallel := r.opts.Parallel
	if parallel < 1 {
		parallel = DefaultParallel
	}
	results := make([]FileResult, len(files))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(parallel, len(files)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = r.uploadFile(ctx, files[i], i+1, len(files))
			}
		}()
	}
	for i := range files {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results
}

// uploadFile brings one file to the complete state, resuming from the saved upload when there is one.
func (r *runner) uploadFile(ctx context.Context, file localFile, n, total int) FileResult {
	result := FileResult{Path: file.Path, Size: file.Size}
	fail := func(err error) FileResult {
		result.Status, result.Error = StatusFailed, plainError(err)
		r.say("[%d/%d] %s: failed. %s", n, total, file.Path, result.Error)
		return result
	}
	entry, known := r.state.get(file.Path, file.Size, file.MTimeNano)
	switch {
	case known && entry.Complete:
		result.Status = StatusSkipped
		r.say("[%d/%d] %s: already uploaded", n, total, file.Path)
		return result
	case file.Size == 0:
		result.Status = StatusEmpty
		r.say("[%d/%d] %s: empty, skipped", n, total, file.Path)
		return result
	}
	if err := r.transfer(ctx, file, entry.UploadURL, entry.Sha256, &result, n, total); err != nil {
		return fail(err)
	}
	result.Status = StatusUploaded
	r.say("[%d/%d] %s: done (%s)", n, total, file.Path, humanBytes(file.Size))
	return result
}

// transfer creates or resumes the upload, sends the missing parts and waits for the server to finish the file.
func (r *runner) transfer(ctx context.Context, file localFile, uploadURL, savedSum string, result *FileResult, n, total int) error {
	// The digest goes to the server at creation and, on resume, proves the file is the one that was started.
	sum, err := hashFile(file.Abs)
	if err != nil {
		return fmt.Errorf("read %s: %w", file.Path, err)
	}
	if uploadURL != "" && savedSum != sum {
		r.say("[%d/%d] %s: changed since the last run, starting again", n, total, file.Path)
		if err := r.terminate(ctx, uploadURL); err != nil {
			return err
		}
		uploadURL = ""
	}
	entry := fileState{Size: file.Size, MTimeNano: file.MTimeNano, Sha256: sum}
	recreated := false
	for {
		if uploadURL == "" {
			created, err := retry(ctx, r.opts.retryBase(), func() (string, error) {
				return r.client.CreateUpload(ctx, r.snap.ID, file.Path, file.Size, sum)
			})
			if err != nil {
				return err
			}
			uploadURL = created
			entry.UploadURL = uploadURL
			if err := r.state.put(file.Path, entry); err != nil {
				return err
			}
		}
		upload, err := retry(ctx, r.opts.retryBase(), func() (Upload, error) { return r.client.GetUpload(ctx, uploadURL) })
		gone := isStatus(err, 404) || (err == nil && upload.State == "failed")
		if gone && !recreated {
			if err := r.terminate(ctx, uploadURL); err != nil {
				return err
			}
			recreated, uploadURL = true, ""
			continue
		}
		if err != nil {
			return err
		}
		switch upload.State {
		case "failed":
			return fmt.Errorf("the server discarded the upload: %s", firstNonEmpty(upload.FailureReason, "no reason given"))
		case "complete":
		default:
			if upload.State == "uploading" && upload.OffsetBytes < file.Size {
				if upload.OffsetBytes > 0 {
					result.Resumed = true
					r.say("[%d/%d] %s: resuming at %s of %s", n, total, file.Path, humanBytes(upload.OffsetBytes), humanBytes(file.Size))
				}
				if err := r.sendParts(ctx, file, uploadURL, upload.OffsetBytes); err != nil {
					return err
				}
			}
			if err := r.awaitComplete(ctx, uploadURL); err != nil {
				return err
			}
		}
		entry.Complete = true
		return r.state.put(file.Path, entry)
	}
}

// terminate cancels an old upload before a new one takes its place.
func (r *runner) terminate(ctx context.Context, uploadURL string) error {
	_, err := retry(ctx, r.opts.retryBase(), func() (struct{}, error) {
		return struct{}{}, r.client.TerminateUpload(ctx, uploadURL)
	})
	return err
}

// hashFile returns the hex SHA-256 of a file.
func hashFile(path string) (string, error) {
	handle, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, handle); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// sendParts sends the file from offset on, one part after another. After any failure it asks the server where it
// stands, so a part that arrived is never sent again.
func (r *runner) sendParts(ctx context.Context, file localFile, uploadURL string, offset int64) error {
	handle, err := os.Open(file.Abs)
	if err != nil {
		return err
	}
	defer handle.Close()
	if info, err := handle.Stat(); err != nil || info.Size() != file.Size || info.ModTime().UnixNano() != file.MTimeNano {
		return errors.New("the file changed while uploading. Run the command again")
	}
	buf := make([]byte, min(r.opts.partSize(), file.Size))
	failures := 0
	for offset < file.Size {
		part := buf[:min(int64(len(buf)), file.Size-offset)]
		if _, err := handle.ReadAt(part, offset); err != nil {
			return fmt.Errorf("read %s: %w", file.Path, err)
		}
		next, err := r.client.AppendPart(ctx, uploadURL, offset, part)
		if err == nil {
			offset, failures = next, 0
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !retryable(err) && !isStatus(err, 409) {
			return err
		}
		if failures++; failures >= maxAttempts {
			return err
		}
		if !isStatus(err, 409) {
			if err := sleep(ctx, backoff(r.opts.retryBase(), failures-1)); err != nil {
				return err
			}
		}
		current, getErr := r.client.GetUpload(ctx, uploadURL)
		if getErr != nil {
			if !retryable(getErr) {
				return getErr
			}
			continue
		}
		if current.State != "uploading" {
			return nil
		}
		if isStatus(err, 409) && current.OffsetBytes == offset {
			// The server holds the same offset but refused the bytes: it has other bytes for this part.
			return errors.New("the file changed since the earlier upload. Delete .yggy-upload-state.json and run again")
		}
		offset = current.OffsetBytes
	}
	return nil
}

// awaitComplete polls until the server has assembled and verified the file.
func (r *runner) awaitComplete(ctx context.Context, uploadURL string) error {
	deadline := time.Now().Add(r.opts.pollTimeout())
	for {
		upload, err := retry(ctx, r.opts.retryBase(), func() (Upload, error) { return r.client.GetUpload(ctx, uploadURL) })
		if err != nil {
			return err
		}
		switch upload.State {
		case "complete":
			return nil
		case "failed":
			return fmt.Errorf("the server discarded the file: %s", firstNonEmpty(upload.FailureReason, "no reason given"))
		}
		if time.Now().After(deadline) {
			return errors.New("the server is still assembling the file. Run the command again later to check")
		}
		if err := sleep(ctx, r.opts.pollEvery()); err != nil {
			return err
		}
	}
}

// retry repeats call with backoff while its error is worth repeating.
func retry[T any](ctx context.Context, base time.Duration, call func() (T, error)) (T, error) {
	for attempt := 0; ; attempt++ {
		value, err := call()
		if err == nil || ctx.Err() != nil || !retryable(err) || attempt+1 >= maxAttempts {
			return value, err
		}
		if err := sleep(ctx, backoff(base, attempt)); err != nil {
			return value, err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// plainError drops the Go wrapper text of a network failure.
func plainError(err error) string {
	if errors.Is(err, context.Canceled) {
		return "interrupted"
	}
	return err.Error()
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, suffix := float64(n), ""
	for _, s := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		suffix = s
		if value < unit {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", value, suffix)
}
