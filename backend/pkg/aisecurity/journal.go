package aisecurity

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"sync"
)

var ErrReplay = errors.New("request nonce already consumed")

type ReplayJournal struct {
	mu       sync.Mutex
	file     *os.File
	lockPath string
	seen     map[string]bool
}

// OpenReplayJournal takes an exclusive process lock. A crash leaves its lock file,
// requiring an operator to confirm the old process stopped before removing that lock.
func OpenReplayJournal(path string) (*ReplayJournal, error) {
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	_ = lock.Close()
	fail := func(e error) (*ReplayJournal, error) { _ = os.Remove(path + ".lock"); return nil, e }
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return fail(err)
	}
	info, err := f.Stat()
	if err != nil || info.Mode().Perm()&0077 != 0 || info.Size() > 32<<20 {
		f.Close()
		return fail(errors.New("unsafe or oversized replay journal"))
	}
	j := &ReplayJournal{file: f, lockPath: path + ".lock", seen: map[string]bool{}}
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 4096)
	for scan.Scan() {
		var record struct {
			Key    string `json:"key"`
			Digest string `json:"digest"`
		}
		if json.Unmarshal(scan.Bytes(), &record) != nil || !digest.MatchString(record.Key) || !digest.MatchString(record.Digest) || j.seen[record.Key] {
			f.Close()
			return fail(errors.New("invalid replay journal"))
		}
		j.seen[record.Key] = true
	}
	if err = scan.Err(); err != nil {
		f.Close()
		return fail(err)
	}
	return j, nil
}
func (j *ReplayJournal) Reserve(app, principal, requestID, requestDigest string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	key := hash([]string{app, principal, requestID})
	if j.seen[key] {
		return ErrReplay
	}
	if len(j.seen) >= 100000 {
		return errors.New("replay journal capacity reached")
	}
	j.seen[key] = true
	raw, _ := json.Marshal(struct {
		Key    string `json:"key"`
		Digest string `json:"digest"`
	}{key, requestDigest})
	raw = append(raw, '\n')
	if _, e := j.file.Write(raw); e != nil {
		return e
	}
	return j.file.Sync()
}
func (j *ReplayJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	err := j.file.Close()
	_ = os.Remove(j.lockPath)
	return err
}

// AppendAudit records the event durably before returning. It never serializes source content.
func AppendAudit(path string) (func(AuditRecord) error, func() error, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return nil, nil, e
	}
	info, e := f.Stat()
	if e != nil || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, nil, errors.New("audit file must be private")
	}
	var mu sync.Mutex
	return func(a AuditRecord) error {
		mu.Lock()
		defer mu.Unlock()
		raw, e := json.Marshal(a)
		if e != nil {
			return e
		}
		raw = append(raw, '\n')
		if _, e = f.Write(raw); e != nil {
			return e
		}
		return f.Sync()
	}, f.Close, nil
}
