// Copyright 2020 VMware, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	regauthn "github.com/google/go-containerregistry/pkg/authn"
	regname "github.com/google/go-containerregistry/pkg/name"
	regv1 "github.com/google/go-containerregistry/pkg/v1"
	regremote "github.com/google/go-containerregistry/pkg/v1/remote"
)

type Opts struct {
	CACertPaths   []string
	VerifyCerts   bool
	Insecure      bool
	EnvAuthPrefix string
}

type Registry struct {
	opts    []regremote.Option
	refOpts []regname.Option
}

func NewRegistry(opts Opts) (Registry, error) {
	keychain := regauthn.NewMultiKeychain(NewEnvKeychain(opts.EnvAuthPrefix), regauthn.DefaultKeychain)
	transport, err := newHTTPTransport(opts)
	if err != nil {
		return Registry{}, err
	}

	var refOpts []regname.Option
	if opts.Insecure {
		refOpts = append(refOpts, regname.Insecure)
	}

	return Registry{
		opts: []regremote.Option{
			regremote.WithTransport(transport),
			regremote.WithAuthFromKeychain(keychain),
		},
		refOpts: refOpts,
	}, nil
}

func (i Registry) Generic(ref regname.Reference) (regv1.Descriptor, error) {
	ref, err := regname.ParseReference(ref.String(), i.refOpts...)
	if err != nil {
		return regv1.Descriptor{}, err
	}

	desc, err := regremote.Get(ref, i.opts...)
	if err != nil {
		return regv1.Descriptor{}, err
	}

	return desc.Descriptor, nil
}

func (i Registry) Image(ref regname.Reference) (regv1.Image, error) {
	ref, err := regname.ParseReference(ref.String(), i.refOpts...)
	if err != nil {
		return nil, err
	}

	return regremote.Image(ref, i.opts...)
}

func (i Registry) WriteImage(ref regname.Reference, img regv1.Image) error {
	ref, err := regname.ParseReference(ref.String(), i.refOpts...)
	if err != nil {
		return err
	}

	err = i.retry(func() error {
		return regremote.Write(ref, img, i.opts...)
	})
	if err != nil {
		return fmt.Errorf("Writing image: %s", err)
	}

	return nil
}

func (i Registry) Index(ref regname.Reference) (regv1.ImageIndex, error) {
	ref, err := regname.ParseReference(ref.String(), i.refOpts...)
	if err != nil {
		return nil, err
	}

	return regremote.Index(ref, i.opts...)
}

func (i Registry) WriteIndex(ref regname.Reference, idx regv1.ImageIndex) error {
	ref, err := regname.ParseReference(ref.String(), i.refOpts...)
	if err != nil {
		return err
	}

	err = i.retry(func() error {
		return regremote.WriteIndex(ref, idx, i.opts...)
	})
	if err != nil {
		return fmt.Errorf("Writing image index: %s", err)
	}

	return nil
}

func (i Registry) WriteTag(dstRef regname.Tag, srcRef regname.Digest) error {
	dstRef, err := regname.NewTag(dstRef.String(), i.refOpts...)
	if err != nil {
		return err
	}

	srcRef, err = regname.NewDigest(srcRef.String(), i.refOpts...)
	if err != nil {
		return err
	}

	err = i.retry(func() error {
		desc, err := regremote.Get(srcRef, i.opts...)
		if err != nil {
			return err
		}

		return regremote.Tag(dstRef, desc, i.opts...)
	})
	if err != nil {
		return fmt.Errorf("Writing image tag: %s", err)
	}

	return nil
}

func (i Registry) ListTags(repo regname.Repository) ([]string, error) {
	repo, err := regname.NewRepository(repo.Name(), i.refOpts...)
	if err != nil {
		return nil, err
	}

	return regremote.List(repo, i.opts...)
}

func newHTTPTransport(opts Opts) (*http.Transport, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}

	if len(opts.CACertPaths) > 0 {
		for _, path := range opts.CACertPaths {
			if certs, err := os.ReadFile(path); err != nil {
				return nil, fmt.Errorf("Reading CA certificates from '%s': %s", path, err)
			} else if ok := pool.AppendCertsFromPEM(certs); !ok {
				return nil, fmt.Errorf("Adding CA certificates from '%s': failed", path)
			}
		}
	}

	// Copied from https://github.com/golang/go/blob/release-branch.go1.12/src/net/http/transport.go#L42-L53
	// We want to use the DefaultTransport but change its TLSClientConfig. There
	// isn't a clean way to do this yet: https://github.com/golang/go/issues/26013
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Use the cert pool with k8s cert bundle appended.
		TLSClientConfig: &tls.Config{
			RootCAs:            pool,
			InsecureSkipVerify: (opts.VerifyCerts == false),
		},
	}, nil
}

func (i Registry) retry(doFunc func() error) error {
	var lastErr error
	for i := 0; i < 5; i++ {
		lastErr = doFunc()
		if lastErr == nil {
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("Retried 5 times: %s", lastErr)
}
