// Copyright (C) 2026 EclipseSource GmbH and others.
//
// This program and the accompanying materials are made available under the
// terms of the MIT License, which is available in the project root.
//
// SPDX-License-Identifier: MIT

package backend

import (
	"reflect"
	"testing"
)

func TestValidateAuthFilePaths(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    []string
		wantErr bool
	}{
		{
			name:  "valid single path",
			input: []string{"auth.json"},
			want:  []string{"auth.json"},
		},
		{
			name:  "valid nested path",
			input: []string{"agent/auth.json"},
			want:  []string{"agent/auth.json"},
		},
		{
			name:  "cleans path",
			input: []string{"agent/../auth.json"},
			want:  []string{"auth.json"},
		},
		{
			name:    "empty path",
			input:   []string{""},
			wantErr: true,
		},
		{
			name:    "absolute path",
			input:   []string{"/etc/passwd"},
			wantErr: true,
		},
		{
			name:    "path traversal",
			input:   []string{"../auth.json"},
			wantErr: true,
		},
		{
			name:    "current directory",
			input:   []string{"."},
			wantErr: true,
		},
		{
			name:  "empty slice",
			input: nil,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateAuthFilePaths(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("unexpected result: got %v want %v", got, tt.want)
			}
		})
	}
}
