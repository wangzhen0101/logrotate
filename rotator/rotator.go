// Copyright (c) 2017, moshee
// Copyright (c) 2017, Josh Rickmar <jrick@devio.us>
// All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are met:
//
// * Redistributions of source code must retain the above copyright notice, this
//   list of conditions and the following disclaimer.
//
// * Redistributions in binary form must reproduce the above copyright notice,
//   this list of conditions and the following disclaimer in the documentation
//   and/or other materials provided with the distribution.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
// AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
// IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
// DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
// FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
// DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
// SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
// CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
// OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
// OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

// Package rotator implements a simple logfile rotator. Logs are read from an
// io.Reader and are written to a file until they reach a specified size. The
// log is then gzipped to another file and truncated.
package rotator

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// nl is a byte slice containing a newline byte.  It is used to avoid creating
// additional allocations when writing newlines to the log file.
var nl = []byte{'\n'}

// A Rotator reads log lines from an input source and writes them to a file,
// splitting it up into gzipped chunks once the filesize reaches a certain
// threshold.
type Rotator struct {
	size      int64
	threshold int64
	maxRolls  int
	filename  string
	in        *bufio.Scanner
	out       *os.File
	tee       bool
	wg        sync.WaitGroup
}

// New returns a new Rotator that is ready to start rotating logs from its
// input.
func New(in io.Reader, filename string, thresholdKB int64, tee bool, maxRolls int) (*Rotator, error) {
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}

	return &Rotator{
		size:      stat.Size(),
		threshold: 1000 * thresholdKB,
		maxRolls:  maxRolls,
		filename:  filename,
		in:        bufio.NewScanner(in),
		out:       f,
		tee:       tee,
	}, nil
}

// Run begins reading lines from the input and rotating logs as necessary.
func (r *Rotator) Run() error {
	// Rotate file immediately if it is already over the size limit.
	if r.size >= r.threshold {
		if err := r.rotate(); err != nil {
			return err
		}
		r.size = 0
	}

	for r.in.Scan() {
		line := r.in.Bytes()

		n, _ := r.out.Write(line)
		m, _ := r.out.Write(nl)

		if r.tee {
			os.Stdout.Write(line)
			os.Stdout.Write(nl)
		}

		r.size += int64(n + m)
		if r.size >= r.threshold {
			if err := r.rotate(); err != nil {
				return err
			}
			r.size = 0
		}
	}

	return nil
}

// Close closes the output logfile.
func (r *Rotator) Close() error {
	err := r.out.Close()
	r.wg.Wait()
	return err
}

func (r *Rotator) rotate() error {
	dir := filepath.Dir(r.filename)
	glob := filepath.Join(dir, filepath.Base(r.filename)+".*")
	existing, err := filepath.Glob(glob)
	if err != nil {
		return err
	}

	maxNum := 0
	for _, name := range existing {
		parts := strings.Split(name, ".")
		if len(parts) < 2 {
			continue
		}
		numIdx := len(parts) - 1
		if parts[numIdx] == "gz" {
			numIdx--
		}
		num, err := strconv.Atoi(parts[numIdx])
		if err != nil {
			continue
		}
		if num > maxNum {
			maxNum = num
		}
	}

	err = r.out.Close()
	if err != nil {
		return err
	}
	rotname := fmt.Sprintf("%s.%d", r.filename, maxNum+1)
	err = os.Rename(r.filename, rotname)
	if err != nil {
		return err
	}
	if r.maxRolls != 0 {
		for n := maxNum + 1 - r.maxRolls; ; n-- {
			err := os.Remove(fmt.Sprintf("%s.%d.gz", r.filename, n))
			if err != nil {
				break
			}
		}
	}
	r.out, err = os.OpenFile(r.filename, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}

	r.wg.Add(1)
	go func() {
		compress(rotname)
		r.wg.Done()
	}()

	return nil
}

func compress(name string) (err error) {
	f, err := os.Open(name)
	if err != nil {
		return err
	}

	defer func() {
		f.Close()
		if err == nil {
			os.Remove(name)
		}
	}()

	arc, err := os.OpenFile(name+".gz", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	z := gzip.NewWriter(arc)
	if _, err = io.Copy(z, f); err != nil {
		return err
	}
	if err = z.Close(); err != nil {
		return err
	}
	return arc.Close()
}
