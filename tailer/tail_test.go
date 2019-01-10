// Copyright 2011 Google Inc. All Rights Reserved.
// This file is available under the Apache license.

package tailer

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/user"
	"path/filepath"
	"sync"
	"testing"
	"time"

	log "github.com/sgtsquiggs/tail/logger"
	"github.com/sgtsquiggs/tail/logline"
	"github.com/sgtsquiggs/tail/testutil"
	"github.com/sgtsquiggs/tail/watcher"

	"github.com/spf13/afero"
)

func makeTestTail(t *testing.T) (*Tailer, chan *logline.LogLine, *watcher.FakeWatcher, afero.Fs, string, func()) {
	fs := afero.NewMemMapFs()
	w := watcher.NewFakeWatcher(nil)
	lines := make(chan *logline.LogLine, 1)
	ta, err := New(lines, fs, w, nil)
	if err != nil {
		t.Fatal(err)
	}
	fs.Mkdir("tail_test", os.ModePerm)
	return ta, lines, w, fs, "/tail_test", func() {}
}

func makeTestTailReal(t *testing.T, prefix string) (*Tailer, chan *logline.LogLine, *watcher.FakeWatcher, afero.Fs, string, func()) {
	if testing.Short() {
		t.Skip("skipping real fs test in short mode")
	}
	dir, err := ioutil.TempDir("", prefix)
	if err != nil {
		t.Fatalf("can't create tempdir: %v", err)
	}

	fs := afero.NewOsFs()
	w := watcher.NewFakeWatcher(nil)
	lines := make(chan *logline.LogLine, 1)
	ta, err := New(lines, fs, w, nil)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Log(err)
		}
	}
	return ta, lines, w, fs, dir, cleanup
}

func TestTail(t *testing.T) {
	ta, _, w, fs, dir, cleanup := makeTestTail(t)
	defer cleanup()

	logfile := filepath.Join(dir, "log")
	f, err := fs.Create(logfile)
	if err != nil {
		t.Error(err)
	}
	defer f.Close()
	defer w.Close()

	err = ta.TailPath(logfile)
	if err != nil {
		t.Fatal(err)
	}
	// Tail also causes the log to be read, so no need to inject an event.

	if _, ok := ta.handles[logfile]; !ok {
		t.Errorf("path not found in files map: %+#v", ta.handles)
	}
}

func TestHandleLogUpdate(t *testing.T) {
	ta, lines, w, fs, dir, cleanup := makeTestTailReal(t, "handle_log_update")
	defer cleanup()

	logfile := filepath.Join(dir, "log")
	f, err := fs.Create(logfile)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	result := []*logline.LogLine{}
	done := make(chan struct{})
	wg := sync.WaitGroup{}
	go func() {
		for line := range lines {
			log.DefaultLogger.Infof("line %v", line)
			result = append(result, line)
			wg.Done()
		}
		close(done)
	}()

	err = ta.TailPath(logfile)
	if err != nil {
		t.Fatal(err)
	}

	wg.Add(4)
	_, err = f.WriteString("a\nb\nc\nd\n")
	if err != nil {
		t.Fatal(err)
	}
	// f.Seek(0, 0) // afero in-memory files share the same offset
	w.InjectUpdate(logfile)

	wg.Wait()
	if err := w.Close(); err != nil {
		t.Log(err)
	}
	<-done

	expected := []*logline.LogLine{
		{logfile, "a"},
		{logfile, "b"},
		{logfile, "c"},
		{logfile, "d"},
	}
	if diff := testutil.Diff(expected, result); diff != "" {
		t.Errorf("result didn't match:\n%s", diff)
	}
}

// TestHandleLogTruncate writes to a file, waits for those
// writes to be seen, then truncates the file and writes some more.
// At the end all lines written must be reported by the tailer.
func TestHandleLogTruncate(t *testing.T) {
	ta, lines, w, fs, dir, cleanup := makeTestTailReal(t, "truncate")
	defer cleanup()

	logfile := filepath.Join(dir, "log")
	f, err := fs.Create(logfile)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	result := []*logline.LogLine{}
	done := make(chan struct{})
	wg := sync.WaitGroup{}
	go func() {
		for line := range lines {
			result = append(result, line)
			wg.Done()
		}
		close(done)
	}()

	if err = ta.TailPath(logfile); err != nil {
		t.Fatal(err)
	}

	wg.Add(3)
	if _, err = f.WriteString("a\nb\nc\n"); err != nil {
		t.Fatal(err)
	}
	//time.Sleep(10 * time.Millisecond)
	w.InjectUpdate(logfile)
	wg.Wait()

	if err = f.Truncate(0); err != nil {
		t.Fatal(err)
	}
	// "File.Truncate" does not change the file offset.
	f.Seek(0, 0)
	w.InjectUpdate(logfile)
	//time.Sleep(10 * time.Millisecond)

	wg.Add(2)
	if _, err = f.WriteString("d\ne\n"); err != nil {
		t.Fatal(err)
	}
	w.InjectUpdate(logfile)
	//time.Sleep(10 * time.Millisecond)

	wg.Wait()
	if err := w.Close(); err != nil {
		t.Log(err)
	}
	<-done

	expected := []*logline.LogLine{
		{logfile, "a"},
		{logfile, "b"},
		{logfile, "c"},
		{logfile, "d"},
		{logfile, "e"},
	}
	if diff := testutil.Diff(expected, result); diff != "" {
		t.Errorf("result didn't match:\n%s", diff)
	}
}

func TestHandleLogUpdatePartialLine(t *testing.T) {
	ta, lines, w, fs, dir, cleanup := makeTestTailReal(t, "log_update_partial_line")
	defer cleanup()

	logfile := filepath.Join(dir, "log")
	f, err := fs.Create(logfile)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	result := []*logline.LogLine{}
	done := make(chan struct{})
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		for line := range lines {
			result = append(result, line)
			wg.Done()
		}
		close(done)
	}()

	err = ta.TailPath(logfile)
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.WriteString("a")
	if err != nil {
		t.Fatal(err)
	}
	//f.Seek(0, 0)
	w.InjectUpdate(logfile)

	//f.Seek(1, 0)
	_, err = f.WriteString("b")
	if err != nil {
		t.Error(err)
	}
	// f.Seek(1, 0)
	w.InjectUpdate(logfile)

	//f.Seek(2, 0)
	_, err = f.WriteString("\n")
	if err != nil {
		t.Error(err)
	}
	//f.Seek(2, 0)
	w.InjectUpdate(logfile)

	wg.Wait()
	w.Close()
	<-done

	expected := []*logline.LogLine{
		{logfile, "ab"},
	}
	diff := testutil.Diff(expected, result)
	if diff != "" {
		t.Errorf("result didn't match:\n%s", diff)
	}

}

func TestTailerOpenRetries(t *testing.T) {
	// Can't force a permission denied error if run as root.
	u, err := user.Current()
	if err != nil {
		t.Skip(fmt.Sprintf("Couldn't determine current user id: %s", err))
	}
	if u.Uid == "0" {
		t.Skip("Skipping test when run as root")
	}
	// Use the real filesystem because afero doesn't implement correct
	// permissions checking on OpenFile in the memfile implementation.
	ta, lines, w, fs, dir, cleanup := makeTestTailReal(t, "retries")
	defer cleanup()

	logfile := filepath.Join(dir, "log")
	if _, err := fs.OpenFile(logfile, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	wg := sync.WaitGroup{}
	wg.Add(1) // lines written
	go func() {
		for range lines {
			wg.Done()
		}
		close(done)
	}()
	ta.AddPattern(logfile)

	if err := ta.TailPath(logfile); err == nil || !os.IsPermission(err) {
		t.Fatalf("Expected a permission denied error here: %s", err)
	}
	//w.InjectUpdate(logfile)
	//time.Sleep(10 * time.Millisecond)
	log.DefaultLogger.Info("remove")
	if err := fs.Remove(logfile); err != nil {
		t.Fatal(err)
	}
	w.InjectDelete(logfile)
	//time.Sleep(10 * time.Millisecond)
	log.DefaultLogger.Info("openfile")
	f, err := fs.OpenFile(logfile, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	w.InjectCreate(logfile)
	//	time.Sleep(10 * time.Millisecond)
	log.DefaultLogger.Info("chmod")
	if err := fs.Chmod(logfile, 0666); err != nil {
		t.Fatal(err)
	}
	w.InjectUpdate(logfile)
	//time.Sleep(10 * time.Millisecond)
	log.DefaultLogger.Info("write string")
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	w.InjectUpdate(logfile)

	wg.Wait()
	if err := w.Close(); err != nil {
		t.Log(err)
	}
	<-done
}

func TestTailerInitErrors(t *testing.T) {
	_, err := New(nil, nil, nil, nil)
	if err == nil {
		t.Error("expected error")
	}
	lines := make(chan *logline.LogLine)
	_, err = New(lines, nil, nil, nil)
	if err == nil {
		t.Error("expected error")
	}
	fs := afero.NewMemMapFs()
	_, err = New(lines, fs, nil, nil)
	if err == nil {
		t.Error("expected error")
	}
	w := watcher.NewFakeWatcher(nil)
	_, err = New(lines, fs, w, nil)
	if err != nil {
		t.Errorf("unexpected error %s", err)
	}
	_, err = New(lines, fs, w, nil, OneShot)
	if err != nil {
		t.Errorf("unexpected error %s", err)
	}
}

func TestHandleLogRotate(t *testing.T) {
	ta, lines, w, fs, dir, cleanup := makeTestTailReal(t, "rotate")
	defer cleanup()

	logfile := filepath.Join(dir, "log")
	f, err := fs.Create(logfile)
	if err != nil {
		t.Fatal(err)
	}

	result := []*logline.LogLine{}
	done := make(chan struct{})
	wg := sync.WaitGroup{}
	go func() {
		for line := range lines {
			result = append(result, line)
			wg.Done()
		}
		close(done)
	}()

	if err := ta.TailPath(logfile); err != nil {
		t.Fatal(err)
	}
	wg.Add(2)
	if _, err = f.WriteString("1\n"); err != nil {
		t.Fatal(err)
	}
	log.DefaultLogger.Info("update")
	w.InjectUpdate(logfile)
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if err = fs.Rename(logfile, logfile+".1"); err != nil {
		t.Fatal(err)
	}
	log.DefaultLogger.Info("delete")
	w.InjectDelete(logfile)
	w.InjectCreate(logfile + ".1")
	f, err = fs.Create(logfile)
	if err != nil {
		t.Fatal(err)
	}
	log.DefaultLogger.Info("create")
	w.InjectCreate(logfile)
	if _, err = f.WriteString("2\n"); err != nil {
		t.Fatal(err)
	}
	log.DefaultLogger.Info("update")
	w.InjectUpdate(logfile)

	wg.Wait()
	w.Close()
	<-done

	expected := []*logline.LogLine{
		{logfile, "1"},
		{logfile, "2"},
	}
	diff := testutil.Diff(expected, result)
	if diff != "" {
		t.Errorf("result didn't match expected:\n%s", diff)
	}
}

func TestHandleLogRotateSignalsWrong(t *testing.T) {
	ta, lines, w, fs, dir, cleanup := makeTestTailReal(t, "rotate wrong")
	defer cleanup()
	logfile := filepath.Join(dir, "log")
	f, err := fs.Create(logfile)
	if err != nil {
		t.Fatal(err)
	}

	result := []*logline.LogLine{}
	done := make(chan struct{})
	wg := sync.WaitGroup{}
	go func() {
		for line := range lines {
			result = append(result, line)
			wg.Done()
		}
		close(done)
	}()

	if err := ta.TailPath(logfile); err != nil {
		t.Fatal(err)
	}
	wg.Add(2)
	if _, err = f.WriteString("1\n"); err != nil {
		t.Fatal(err)
	}
	log.DefaultLogger.Info("update")
	w.InjectUpdate(logfile)
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if err = fs.Rename(logfile, logfile+".1"); err != nil {
		t.Fatal(err)
	}
	// Forcibly remove it from the fake filesystem because afero bugs
	fs.Remove(logfile)
	// No delete signal yet
	f, err = fs.Create(logfile)
	if err != nil {
		t.Fatal(err)
	}
	log.DefaultLogger.Info("create")
	w.InjectCreate(logfile)

	time.Sleep(1 * time.Millisecond)
	log.DefaultLogger.Info("delete")
	w.InjectDelete(logfile)

	if _, err = f.WriteString("2\n"); err != nil {
		t.Fatal(err)
	}
	log.DefaultLogger.Info("update")
	w.InjectUpdate(logfile)

	wg.Wait()
	w.Close()
	<-done

	expected := []*logline.LogLine{
		{logfile, "1"},
		{logfile, "2"},
	}
	diff := testutil.Diff(expected, result)
	if diff != "" {
		t.Errorf("result didn't match expected:\n%s", diff)
	}
}
