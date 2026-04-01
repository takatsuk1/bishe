package chat

import "testing"

func TestBearerTokenFromHeader(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		want    string
		wantErr bool
	}{
		{name: "valid", header: "Bearer token123", want: "token123", wantErr: false},
		{name: "missing prefix", header: "token123", wantErr: true},
		{name: "empty", header: "", wantErr: true},
		{name: "blank token", header: "Bearer   ", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bearerTokenFromHeader(tc.header)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got token=%q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("token mismatch: got=%q want=%q", got, tc.want)
			}
		})
	}
}
