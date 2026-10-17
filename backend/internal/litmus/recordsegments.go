package litmus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

func recordingSegments(leg Leg) []RecordSegment {
	if len(leg.RecordSegments) > 0 {
		return leg.RecordSegments
	}
	if leg.RecordStartedAt.IsZero() {
		return nil
	}
	return []RecordSegment{{File: filepath.Join(leg.Name, leg.Name+".mp4"), StartedAt: leg.RecordStartedAt}}
}

func segmentCaptureDir(runDir, leg, file string) string {
	if file == filepath.Join(leg, leg+".mp4") {
		return filepath.Join(runDir, leg)
	}
	return filepath.Join(runDir, leg, strings.TrimSuffix(filepath.Base(file), ".mp4"))
}

// Keep the first input until both the joined file and its metadata are saved.
// A failed save can then be resumed without concatenating the joined file again.
func segmentSourcePath(runDir, leg, file string) string {
	if file == filepath.Join(leg, leg+".mp4") {
		backup := filepath.Join(runDir, leg, leg+".seg1.mp4")
		if fileExists(backup) {
			return backup
		}
	}
	return filepath.Join(runDir, file)
}

func segmentOffset(a actionRec, segment RecordSegment, length time.Duration) (time.Duration, bool) {
	if a.RecordSegment != "" {
		if a.RecordSegment != segment.File {
			return 0, false
		}
		local := a.SegmentOffsetMS
		if local == nil {
			local = a.RecordOffsetMS
		}
		if local != nil {
			offset := time.Duration(*local) * time.Millisecond
			return offset, offset >= 0 && offset <= length
		}
	}
	if a.RecordSegment == "" && a.RecordOffsetMS != nil && segment.File == filepath.Join(a.onLeg(), a.onLeg()+".mp4") {
		offset := time.Duration(*a.RecordOffsetMS) * time.Millisecond
		return offset, offset >= 0 && offset <= length
	}
	at := a.SentAt
	if at.IsZero() {
		at = a.At
	}
	offset := at.Sub(segment.StartedAt)
	return offset, offset >= 0 && offset <= length
}

func segmentActions(actions []actionRec, leg string, segment RecordSegment, length time.Duration) []actionRec {
	var out []actionRec
	unrecorded := unrecordedActions(actions, leg)
	for _, a := range actions {
		if a.onLeg() != leg || unrecorded[a.Seq] {
			continue
		}
		if offset, ok := segmentOffset(a, segment, length); ok {
			ms := offset.Milliseconds()
			a.RecordOffsetMS = &ms
			out = append(out, a)
		}
	}
	return out
}

// An absent segment after a failed recorder restart is an evidence gap, even
// when its wall time overlaps an earlier segment after a clock adjustment.
func unrecordedActions(actions []actionRec, leg string) map[int]bool {
	out := map[int]bool{}
	gap := false
	for _, a := range actions {
		if a.onLeg() != leg {
			continue
		}
		if a.Action == "relaunch" {
			gap = a.RecordSegment == ""
		}
		if gap && a.RecordSegment == "" {
			out[a.Seq] = true
		}
	}
	return out
}

// Compare decoder parameters, including the codec headers, before choosing
// stream copy. Screen recordings contain one video stream and no audio.
func (s *Service) recordingStream(ctx context.Context, path string) (json.RawMessage, error) {
	probe := "ffprobe"
	if _, err := exec.LookPath(probe); err != nil {
		probe = homebrewFFprobe
	}
	var streams []json.RawMessage
	err := videoStep(ctx, filepath.Base(filepath.Dir(filepath.Dir(path))), filepath.Base(filepath.Dir(path)), "stream-probe", 15*time.Second, func(ctx context.Context) error {
		if s.runner == nil {
			return fmt.Errorf("recording probe runner is unavailable")
		}
		data, err := s.runner.Run(ctx, "", nil, probe, "-v", "error", "-show_entries",
			"stream=codec_name,codec_type,codec_tag_string,profile,level,width,height,pix_fmt,sample_aspect_ratio,r_frame_rate,time_base,extradata_hash", "-show_data_hash", "sha256", "-of", "json", path)
		if err != nil {
			return err
		}
		var result struct{ Streams []json.RawMessage }
		if err := json.Unmarshal(data, &result); err != nil {
			return err
		}
		if len(result.Streams) != 1 {
			return fmt.Errorf("expected one video stream, got %d streams", len(result.Streams))
		}
		var stream struct {
			CodecType string `json:"codec_type"`
		}
		if err := json.Unmarshal(result.Streams[0], &stream); err != nil {
			return err
		}
		if stream.CodecType != "video" {
			return fmt.Errorf("expected a video stream, got %q", stream.CodecType)
		}
		streams = result.Streams
		return nil
	})
	if err != nil {
		return nil, err
	}
	return streams[0], nil
}

func (s *Service) joinRecordingSegments(ctx context.Context, runID string, leg Leg) error {
	runDir := filepath.Join(s.store.root, runID)
	lengths := make([]time.Duration, len(leg.RecordSegments))
	var list strings.Builder
	var total time.Duration
	var firstStream any
	copyStreams := true
	for i, segment := range leg.RecordSegments {
		path := segmentSourcePath(runDir, leg.Name, segment.File)
		duration, err := s.probeVideo(ctx, path, 0)
		if err != nil {
			return err
		}
		lengths[i] = time.Duration(duration * float64(time.Second))
		total += lengths[i]
		stream, err := s.recordingStream(ctx, path)
		if err != nil {
			return err
		}
		var params any
		if err := json.Unmarshal(stream, &params); err != nil {
			return err
		}
		if i == 0 {
			firstStream = params
		} else if !reflect.DeepEqual(firstStream, params) {
			copyStreams = false
		}
		fmt.Fprintf(&list, "file '%s'\n", strings.ReplaceAll(path, "'", `'\''`))
	}
	listPath := filepath.Join(runDir, leg.Name, "segments-concat.txt")
	joined := filepath.Join(runDir, leg.Name, leg.Name+".joined.mp4")
	err := videoStep(ctx, runID, leg.Name, "join", recordingEncodeTimeout(recordingOutputFrameCount(total)), func(ctx context.Context) error {
		if err := os.WriteFile(listPath, []byte(list.String()), 0o600); err != nil {
			return err
		}
		args := []string{"-y", "-f", "concat", "-safe", "0", "-i", listPath}
		if copyStreams {
			args = append(args, "-c", "copy")
		} else {
			args = screencastArgs(listPath, joined, total)
			return s.media.Run(ctx, args...)
		}
		return s.media.Run(ctx, append(args, "-movflags", "+faststart", joined)...)
	})
	if err != nil {
		return err
	}
	if _, err := s.probeVideo(ctx, joined, total); err != nil {
		return err
	}
	if err := s.joinActionOffsets(runDir, leg, lengths); err != nil {
		return fmt.Errorf("join action offsets: %w", err)
	}
	first := recordingPath(runDir, leg.Name)
	backup := filepath.Join(runDir, leg.Name, leg.Name+".seg1.mp4")
	if !fileExists(backup) {
		if err := os.Rename(first, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(joined, first); err != nil {
		return err
	}
	if _, err := s.update(runID, func(cur *Run) {
		for i := range cur.Legs {
			if cur.Legs[i].Name == leg.Name {
				cur.Legs[i].RecordJoined = true
			}
		}
	}); err != nil {
		return fmt.Errorf("save joined recording: %w", err)
	}
	for i, segment := range leg.RecordSegments {
		path := filepath.Join(runDir, segment.File)
		if i == 0 {
			path = backup
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("joined recording saved, remove segment %s: %w", segment.File, err)
		}
	}
	return nil
}

func (s *Service) joinActionOffsets(runDir string, leg Leg, lengths []time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	actions, err := readPointerActions(runDir)
	if err != nil {
		return err
	}
	var data bytes.Buffer
	unrecorded := unrecordedActions(actions, leg.Name)
	for _, a := range actions {
		if a.onLeg() == leg.Name {
			matched := false
			if !unrecorded[a.Seq] {
				var prior time.Duration
				for i, segment := range leg.RecordSegments {
					if offset, ok := segmentOffset(a, segment, lengths[i]); ok {
						local, joined := offset.Milliseconds(), (prior + offset).Milliseconds()
						a.RecordSegment, a.SegmentOffsetMS, a.RecordOffsetMS = segment.File, &local, &joined
						matched = true
						break
					}
					prior += lengths[i]
				}
			}
			if !matched {
				a.RecordSegment = ""
				a.SegmentOffsetMS = nil
				a.RecordOffsetMS = nil
			}
		}
		if err := json.NewEncoder(&data).Encode(a); err != nil {
			return err
		}
	}
	return writeAtomic(filepath.Join(runDir, "actions.jsonl"), data.Bytes())
}
