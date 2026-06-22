package cleura

import "testing"

func TestDefaultAPIURL(t *testing.T) {
	tests := []struct {
		cloud   string
		want    string
		wantErr bool
	}{
		{cloud: "public", want: "https://rest.cleura.cloud"},
		{cloud: "compliant", want: "https://rest.compliant.cleura.cloud"},
		{cloud: "acme-corp", wantErr: true},
		{cloud: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.cloud, func(t *testing.T) {
			got, err := DefaultAPIURL(tt.cloud)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
