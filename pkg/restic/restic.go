package restic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	v1 "github.com/garethgeorge/resticui/gen/go/v1"
	"github.com/hashicorp/go-multierror"
)

type Repo struct {
	mu sync.Mutex
	cmd string
	repo *v1.Repo
	initialized bool

	extraArgs []string
	extraEnv []string
}

func NewRepo(repo *v1.Repo, opts ...GenericOption) *Repo {
	opt := &GenericOpts{}
	for _, o := range opts {
		o(opt)
	}

	return &Repo{
		cmd: "restic", // TODO: configurable binary path
		repo: repo,
		initialized: false,
		extraArgs: opt.extraArgs,
		extraEnv: opt.extraEnv,
	}
}

func (r *Repo) buildEnv() []string {
	env := []string{
		"RESTIC_REPOSITORY=" + r.repo.GetUri(),
		"RESTIC_PASSWORD=" + r.repo.GetPassword(),
	}
	env = append(env, r.extraEnv...)
	env = append(env, r.repo.GetEnv()...)
	return env
}

// init initializes the repo, the command will be cancelled with the context.
func (r *Repo) init(ctx context.Context) error {
	if r.initialized {
		return nil
	}

	var args = []string{"init", "--json"}
	args = append(args, r.extraArgs...)

	cmd := exec.CommandContext(ctx, r.cmd, args...)
	cmd.Env = append(cmd.Env, r.buildEnv()...)

	if output, err := cmd.CombinedOutput(); err != nil {
		return NewCmdError(cmd, output, err)
	}

	r.initialized = true
	return nil
}

func (r *Repo) Init(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.initialized = false
	return r.init(ctx)
}

func (r *Repo) Backup(ctx context.Context, progressCallback func(*BackupProgressEntry), opts ...BackupOption) (*BackupProgressEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.init(ctx); err != nil {
		return nil, fmt.Errorf("failed to initialize repo: %w", err)
	}
	
	opt := &BackupOpts{}
	for _, o := range opts {
		o(opt)
	}

	for _, p := range opt.paths {
		if _, err := os.Stat(p); err != nil {
			return nil, fmt.Errorf("path %s does not exist: %w", p, err)
		}
	}

	args := []string{"backup", "--json", "--exclude-caches"}
	args = append(args, r.extraArgs...)
	args = append(args, opt.paths...)
	args = append(args, opt.extraArgs...)

	reader, writer := io.Pipe()

	cmd := exec.CommandContext(ctx, r.cmd, args...)
	cmd.Env = append(cmd.Env, r.buildEnv()...)
	cmd.Stderr = writer
	cmd.Stdout = writer

	if err := cmd.Start(); err != nil {
		return nil, NewCmdError(cmd, nil, err)
	}
	
	var wg sync.WaitGroup
	var summary *BackupProgressEntry
	var cmdErr error 
	var readErr error
	
	wg.Add(1)
	go func() {
		defer wg.Done()
		var err error
		summary, err = readBackupProgressEntries(cmd, reader, progressCallback)
		if err != nil {
			readErr = fmt.Errorf("processing command output: %w", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer writer.Close()
		defer wg.Done()
		if err := cmd.Wait(); err != nil {
			cmdErr = NewCmdError(cmd, nil, err)
		}
	}()

	wg.Wait()
	
	var err error
	if cmdErr != nil || readErr != nil {
		err = multierror.Append(nil, cmdErr, readErr)
	}
	return summary, err
}

func (r *Repo) Snapshots(ctx context.Context, opts ...GenericOption) ([]*Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.init(ctx); err != nil {
		return nil, fmt.Errorf("failed to initialize repo: %w", err)
	}

	opt := resolveOpts(opts)

	args := []string{"snapshots", "--json"}
	args = append(args, r.extraArgs...)
	args = append(args, opt.extraArgs...)

	cmd := exec.CommandContext(ctx, r.cmd, args...)
	cmd.Env = append(cmd.Env, r.buildEnv()...)
	cmd.Env = append(cmd.Env, opt.extraEnv...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, NewCmdError(cmd, output, err)
	}

	var snapshots []*Snapshot
	if err := json.Unmarshal(output, &snapshots); err != nil {
		return nil, NewCmdError(cmd, output, fmt.Errorf("command output is not valid JSON: %w", err))
	}

	return snapshots, nil
}

func (r *Repo) ListDirectory(ctx context.Context, snapshot string, path string, opts ...GenericOption) (*Snapshot, []*LsEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if path == "" {
		// an empty path can trigger very expensive operations (e.g. iterates all files in the snapshot)
		return nil, nil, errors.New("path must not be empty")
	}

	if err := r.init(ctx); err != nil {
		return nil, nil, fmt.Errorf("failed to initialize repo: %w", err)
	}

	opt := resolveOpts(opts)

	args := []string{"ls", "--json", snapshot, path}
	args = append(args, r.extraArgs...)
	args = append(args, opt.extraArgs...)

	cmd := exec.CommandContext(ctx, r.cmd, args...)
	cmd.Env = append(cmd.Env, r.buildEnv()...)
	cmd.Env = append(cmd.Env, opt.extraEnv...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, nil, NewCmdError(cmd, output, err)
	}


	snapshots, entries, err := readLs(bytes.NewBuffer(output))
	if err != nil {
		return nil, nil, NewCmdError(cmd, output, err)
	}

	return snapshots, entries, nil
}

type BackupOpts struct {
	paths []string
	extraArgs []string
}

type BackupOption func(opts *BackupOpts)

func WithBackupPaths(paths ...string) BackupOption {
	return func(opts *BackupOpts) {
		opts.paths = append(opts.paths, paths...)
	}
}

func WithBackupExcludes(excludes ...string) BackupOption {
	return func(opts *BackupOpts) {
		for _, exclude := range excludes {
			opts.extraArgs = append(opts.extraArgs, "--exclude", exclude)
		}
	}
}

func WithBackupTags(tags ...string) BackupOption {
	return func(opts *BackupOpts) {
		for _, tag := range tags {
			opts.extraArgs = append(opts.extraArgs, "--tag", tag)
		}
	}
}

type GenericOpts struct {
	extraArgs []string
	extraEnv []string
}

func resolveOpts(opts []GenericOption) *GenericOpts {
	opt := &GenericOpts{}
	for _, o := range opts {
		o(opt)
	}
	return opt
}

type GenericOption func(opts *GenericOpts)

func WithFlags(flags ...string) GenericOption {
	return func(opts *GenericOpts) {
		opts.extraArgs = append(opts.extraArgs, flags...)
	}
}

func WithTags(tags ...string) GenericOption {
	return func(opts *GenericOpts) {
		for _, tag := range tags {
			opts.extraArgs = append(opts.extraArgs, "--tag", tag)
		}
	}
}

func WithEnv(env ...string) GenericOption {
	return func(opts *GenericOpts) {
		opts.extraEnv = append(opts.extraEnv, env...)
	}
}

var EnvToPropagate = []string{"PATH", "HOME", "XDG_CACHE_HOME"}
func WithPropagatedEnvVars(extras ...string) GenericOption {
	var extension []string

	for _, env := range EnvToPropagate {
		if val, ok := os.LookupEnv(env); ok {
			extension = append(extension, env + "=" + val)
		}
	}

	return WithEnv(extension...)
}