// Copyright 2012 The Go-MySQL-Driver Authors. All rights reserved.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at http://mozilla.org/MPL/2.0/.
//
// MODIFIED by semantic-operator: guards the added
// authentication_openid_connect_client plugin. If an upstream rebase drops or
// changes the plugin case, these tests fail. The wire format must stay
// [1-byte capability][length-encoded token length][token], which is what
// StarRocks JWTAuthenticationProvider reads.

package mysql

import (
	"bytes"
	"testing"
)

func TestAuthOpenIDConnectWireFormat(t *testing.T) {
	// A real JWT is several hundred bytes, long enough to force a multi-byte
	// length-encoded prefix, so this exercises the non-fast lenenc path.
	token := bytes.Repeat([]byte("X"), 300)
	mc := &mysqlConn{cfg: NewConfig()}
	mc.cfg.Passwd = string(token)

	authResp, err := mc.auth(nil, "authentication_openid_connect_client")
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	if authResp[0] != 0x01 {
		t.Errorf("capability flag = 0x%02x, want 0x01", authResp[0])
	}
	wantLEI := appendLengthEncodedInteger(nil, uint64(len(token)))
	if !bytes.Equal(authResp[1:1+len(wantLEI)], wantLEI) {
		t.Errorf("length prefix = %v, want %v", authResp[1:1+len(wantLEI)], wantLEI)
	}
	if !bytes.Equal(authResp[1+len(wantLEI):], token) {
		t.Errorf("token body mismatch (got len %d, want %d)", len(authResp[1+len(wantLEI):]), len(token))
	}
}

func TestAuthOpenIDConnectShortToken(t *testing.T) {
	// A short token uses a single-byte lenenc prefix.
	mc := &mysqlConn{cfg: NewConfig()}
	mc.cfg.Passwd = "short.jwt.token"

	authResp, err := mc.auth(nil, "authentication_openid_connect_client")
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	if authResp[0] != 0x01 {
		t.Errorf("capability flag = 0x%02x, want 0x01", authResp[0])
	}
	if int(authResp[1]) != len(mc.cfg.Passwd) {
		t.Errorf("lenenc byte = %d, want %d", authResp[1], len(mc.cfg.Passwd))
	}
	if !bytes.Equal(authResp[2:], []byte(mc.cfg.Passwd)) {
		t.Errorf("token body mismatch: got %q", authResp[2:])
	}
}

func TestAuthOpenIDConnectEmptyTokenFailsFast(t *testing.T) {
	// An empty password means no JWT was supplied. Fail on the client for a
	// clearer error than the eventual server-side access-denied packet.
	mc := &mysqlConn{cfg: NewConfig()}
	mc.cfg.Passwd = ""

	if _, err := mc.auth(nil, "authentication_openid_connect_client"); err == nil {
		t.Fatal("expected an error for an empty JWT, got nil")
	}
}
