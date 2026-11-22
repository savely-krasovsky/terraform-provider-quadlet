// Copyright Savely Krasovsky 2026
// SPDX-License-Identifier: MPL-2.0

package remote

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/savely-krasovsky/terraform-provider-quadlet/internal/host"
	"github.com/savely-krasovsky/terraform-provider-quadlet/internal/local"
)

func TestHostLockSerializesIndependentConnections(t *testing.T) {
	config := testServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	first, err := Connect(ctx, config)
	require.NoError(t, err)

	defer first.Close()

	filename := filepath.Join(t.TempDir(), "config.lock")
	_, release, err := first.Lock(ctx, filename, true)
	require.NoError(t, err)

	defer release()

	second, err := Connect(ctx, config)
	require.NoError(t, err)

	defer second.Close()

	waiting, stop := context.WithTimeout(ctx, 150*time.Millisecond)
	defer stop()

	_, _, err = second.Lock(waiting, filename, true)
	require.Error(t, err)
	require.ErrorIs(t, waiting.Err(), context.DeadlineExceeded)

	// Releasing a deployment must not close a connection used by another resource.
	unrelated, err := Connect(ctx, config)
	require.NoError(t, err)

	defer unrelated.Close()

	release()

	_, err = unrelated.Run(ctx, host.Command{Name: "true"})
	require.NoError(t, err)

	_, unlock, err := unrelated.Lock(ctx, filename, true)
	require.NoError(t, err)
	unlock()
}

func TestLostConnectionReleasesTheHostLock(t *testing.T) {
	config := testServer(t)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	first, err := Connect(ctx, config)
	require.NoError(t, err)

	defer first.Close()

	filename := filepath.Join(t.TempDir(), "config.lock")
	lease, release, err := first.Lock(ctx, filename, true)
	require.NoError(t, err)

	defer release()

	first.Close()

	select {
	case <-lease.Done():
	case <-ctx.Done():
		t.Fatal("lost connection did not cancel the deployment")
	}

	second, err := Connect(ctx, config)
	require.NoError(t, err)

	defer second.Close()

	_, unlock, err := second.Lock(ctx, filename, true)
	require.NoError(t, err)
	unlock()
}

func TestSSHAndLocalUseTheSameHostLock(t *testing.T) {
	config := testServer(t)
	name := filepath.Join(t.TempDir(), "config.lock")

	for _, sshFirst := range []bool{true, false} {
		var first, second host.Session

		ssh, err := Connect(t.Context(), config)
		require.NoError(t, err)

		defer ssh.Close()

		localHost, err := local.Connect(t.Context())
		require.NoError(t, err)

		defer localHost.Close()

		if sshFirst {
			first, second = ssh, localHost
		} else {
			first, second = localHost, ssh
		}

		_, release, err := first.Lock(t.Context(), name, true)
		require.NoError(t, err)

		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		_, _, err = second.Lock(ctx, name, true)
		require.Error(t, err)
		require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
		cancel()
		release()
	}
}

func TestSSHAndLocalShareReadLeasesAndExcludeWriters(t *testing.T) {
	config := testServer(t)
	name := filepath.Join(t.TempDir(), "config.lock")

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	localReader, err := local.Connect(ctx)
	require.NoError(t, err)

	defer localReader.Close()

	_, releaseLocal, err := localReader.Lock(ctx, name, false)
	require.NoError(t, err)

	defer releaseLocal()

	sshReader, err := Connect(ctx, config)
	require.NoError(t, err)

	defer sshReader.Close()

	_, releaseSSH, err := sshReader.Lock(ctx, name, false)
	require.NoError(t, err)

	defer releaseSSH()

	for _, release := range []func(){releaseSSH, releaseLocal} {
		writer, err := local.Connect(ctx)
		require.NoError(t, err)

		wait, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		_, unlock, err := writer.Lock(wait, name, true)

		cancel()

		if unlock != nil {
			unlock()
		}

		writer.Close()
		require.ErrorIs(t, err, context.DeadlineExceeded)
		release()
	}

	writer, err := local.Connect(ctx)
	require.NoError(t, err)

	defer writer.Close()

	_, releaseWriter, err := writer.Lock(ctx, name, true)
	require.NoError(t, err)

	defer releaseWriter()

	reader, err := Connect(ctx, config)
	require.NoError(t, err)

	defer reader.Close()

	wait, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()

	_, _, err = reader.Lock(wait, name, false)
	require.Error(t, err)
	require.ErrorIs(t, wait.Err(), context.DeadlineExceeded)
}
