package mediaprobe

import (
	"context"
	"github.com/hjh006/scarlet-plume/backend/pkg/aisecurity"
	"reflect"
	"testing"
)

func TestRealProbeProtocolCanonicalizationAndCancellation(t *testing.T) {
	catalog := aisecurity.Config{Models: []aisecurity.Model{{Enabled: true, Media: &aisecurity.MediaPolicy{AllowedMIMETypes: []string{"image/jpeg", "image/png", "audio/wav"}}}, {Enabled: false, Media: &aisecurity.MediaPolicy{AllowedMIMETypes: []string{"unsupported"}}}}}
	if formats := Formats(catalog); !reflect.DeepEqual(formats, []string{"image/png", "audio/wav"}) {
		t.Fatal("JPEG was not normalized for probing", formats)
	}
	calls := 0
	processor := func(ctx context.Context, a aisecurity.MediaAsset) (aisecurity.MediaReport, error) {
		calls++
		if len(a.Data) == 0 || len(a.Data) > 65536 {
			t.Fatal("unbounded probe")
		}
		return aisecurity.MediaReport{Digest: a.Digest, MIMEType: a.MIMEType, InspectionVersion: aisecurity.MediaInspectionVersion, Complete: true}, nil
	}
	if err := Check(t.Context(), processor, []string{"image/png", "audio/wav"}); err != nil || calls != 2 {
		t.Fatal("canonical probe rejected", calls, err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if Check(cancelled, processor, []string{"image/png"}) == nil || calls != 2 {
		t.Fatal("cancelled probe executed")
	}
	old := func(ctx context.Context, a aisecurity.MediaAsset) (aisecurity.MediaReport, error) {
		r, e := processor(ctx, a)
		r.InspectionVersion = aisecurity.LegacyMediaInspectionVersion
		return r, e
	}
	if Check(t.Context(), old, []string{"image/png"}) == nil {
		t.Fatal("old scanner declared ready")
	}
	if Check(t.Context(), processor, []string{"image/png", "audio/wav", "image/png"}) == nil {
		t.Fatal("unbounded probes accepted")
	}
}
