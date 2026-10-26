package media

import (
	"math"
	"testing"
)

func sine(n int, hz float64, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * math.Sin(2*math.Pi*hz*float64(i)/MixRate))
	}
	return out
}

func peak(f []int16) int {
	p := 0
	for _, v := range f {
		if a := int(math.Abs(float64(v))); a > p {
			p = a
		}
	}
	return p
}

// TestMixFramesExcludesSelf fails if a talker hears its own voice, if the
// others do not hear it, or if two talkers are not both summed for a
// listener.
func TestMixFramesExcludesSelf(t *testing.T) {
	a, b := sine(MixFrame, 440, 8000), sine(MixFrame, 880, 6000)
	got := MixFrames(map[string][]int16{"a": a, "b": b}, []string{"a", "b", "c"}, MixFrame)
	if p := peak(got["a"]); p < 5000 || p > 6200 {
		t.Fatalf("a hears peak %d; want only b's voice (about 6000)", p)
	}
	if p := peak(got["b"]); p < 7000 || p > 8200 {
		t.Fatalf("b hears peak %d; want only a's voice (about 8000)", p)
	}
	sum := make([]int16, MixFrame)
	copy(sum, a)
	AddInto(sum, b)
	for i, v := range got["c"] {
		if v != sum[i] {
			t.Fatalf("listener sample %d = %d, want the sum %d", i, v, sum[i])
		}
	}
}

// TestMixFramesGateAndLimit fails if idle microphones add noise, if more
// than MaxTalkers are summed, or if a loud sum wraps instead of clipping.
func TestMixFramesGateAndLimit(t *testing.T) {
	quiet := sine(MixFrame, 440, 20) // far below the gate
	got := MixFrames(map[string][]int16{"q": quiet}, []string{"x"}, MixFrame)
	if peak(got["x"]) != 0 {
		t.Fatal("a below-gate source leaked into the mix")
	}
	in := map[string][]int16{}
	for i, id := range []string{"a", "b", "c", "d", "e", "f"} {
		in[id] = sine(MixFrame, 300+float64(i)*50, 3000+float64(i)*100) // f is loudest
	}
	got = MixFrames(in, []string{"x"}, MixFrame)
	want := make([]int16, MixFrame)
	for _, id := range []string{"f", "e", "d", "c"} {
		AddInto(want, in[id])
	}
	for i := range want {
		if got["x"][i] != want[i] {
			t.Fatalf("sample %d = %d, want the sum of the four loudest %d", i, got["x"][i], want[i])
		}
	}
	loud := map[string][]int16{"a": sine(MixFrame, 440, 30000), "b": sine(MixFrame, 440, 30000)}
	if p := peak(MixFrames(loud, []string{"x"}, MixFrame)["x"]); p < math.MaxInt16 {
		t.Fatalf("a loud sum peaks at %d, want clipping at %d", p, math.MaxInt16)
	}
}

// TestMixFramesSilence fails if a lone party gets anything but silence.
func TestMixFramesSilence(t *testing.T) {
	got := MixFrames(map[string][]int16{"a": sine(MixFrame, 440, 8000)}, []string{"a"}, MixFrame)
	if peak(got["a"]) != 0 {
		t.Fatal("a lone talker heard itself")
	}
}

func TestSampleConversion(t *testing.T) {
	in := []int16{0, 1, -1, 32767, -32768, 1234}
	back := PCMToSamples(SamplesToPCM(in))
	for i := range in {
		if back[i] != in[i] {
			t.Fatalf("sample %d round-trips to %d, want %d", i, back[i], in[i])
		}
	}
}

// TestWidebandResampleSeam fails if a 16 kHz party's audio is not brought to
// the mixing rate (and back) with its length scaled: the seam wideband
// codecs use.
func TestWidebandResampleSeam(t *testing.T) {
	wide := SamplesToPCM(sine(320, 440, 8000)) // 20 ms at 16 kHz
	narrow, err := ToConfRate(wide, 16000)
	if err != nil {
		t.Fatal(err)
	}
	if len(narrow) != MixFrame {
		t.Fatalf("resampled to %d samples, want %d", len(narrow), MixFrame)
	}
	if peak(narrow) < 6000 {
		t.Fatalf("resampled peak %d: the tone was lost", peak(narrow))
	}
	up, err := FromConfRate(narrow, 16000)
	if err != nil || len(up) != 320 {
		t.Fatalf("back to 16 kHz = %d samples, %v; want 320", len(up), err)
	}
}

func TestConfTonesRender(t *testing.T) {
	for _, tone := range []ConfTone{ToneJoin, ToneLeave, TonePIN, ToneMuted, ToneUnmute, ToneLocked, ToneDenied, ToneAdmit} {
		s := ConfToneSamples(tone)
		if len(s) < MixFrame || peak(s) < 3000 {
			t.Fatalf("tone %d renders %d samples, peak %d", tone, len(s), peak(s))
		}
	}
	if w := WaitLoop(); len(w) != 6*MixRate || peak(w) == 0 {
		t.Fatalf("wait loop is %d samples", len(w))
	}
}

func TestConfRecorder(t *testing.T) {
	r := NewConfRecorder(2 * MixFrame * 2)
	r.Append(sine(MixFrame, 440, 8000))
	r.Append(sine(MixFrame, 440, 8000))
	r.Append(sine(MixFrame, 440, 8000)) // over the bound
	if !r.Over() {
		t.Fatal("the bound was not reported")
	}
	wav, ms, err := r.Result()
	if err != nil || ms != 40 {
		t.Fatalf("result = %d ms, %v; want 40 ms", ms, err)
	}
	pcm, rate, err := DecodeWAV(wav)
	if err != nil || rate != MixRate || len(pcm) != 2*2*MixFrame {
		t.Fatalf("wav = %d bytes at %d Hz, %v", len(pcm), rate, err)
	}
	if _, _, err := NewConfRecorder(0).Result(); err == nil {
		t.Fatal("an empty recording was returned")
	}
}
