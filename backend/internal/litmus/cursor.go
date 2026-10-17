package litmus

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const (
	pointerGlide = 200 * time.Millisecond
	pointerPulse = 400 * time.Millisecond
	pointerFrame = time.Second / recordingFPS // 25 fps, including animation during held screencast frames
)

// pointerAction excludes keyboard input and wheel events even when they carry x/y.
func pointerAction(action string, args []string) (bool, int) {
	switch action {
	case "click", "tap", "check", "uncheck":
		return true, 1
	case "dblclick":
		return true, 2
	case "hover":
		return true, 0
	case "cdp":
		if len(args) < 2 || args[0] != "Input.dispatchMouseEvent" {
			return false, 0
		}
		var event struct {
			Type       string `json:"type"`
			ClickCount int    `json:"clickCount"`
		}
		params := rawCDPParams(args)
		if json.Unmarshal([]byte(params), &event) != nil || event.Type == "mouseWheel" {
			return false, 0
		}
		if event.Type == "mousePressed" {
			return true, min(max(event.ClickCount, 1), 2)
		}
		return true, 0
	}
	return false, 0
}

type pointerEvent struct {
	at    time.Duration
	point *clickAt
	rings int
}

type pointerRing struct {
	x, y     float64
	count    int
	progress float64
}

type pointerState struct {
	visible bool
	x, y    float64
	rings   []pointerRing
}

func validPointer(p *clickAt) bool {
	if p == nil {
		return false
	}
	for _, v := range []float64{p.X, p.Y, p.W, p.H} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return p.W > 0 && p.H > 0 && p.X >= 0 && p.Y >= 0 && p.X < p.W && p.Y < p.H
}

func pointerEvents(actions []actionRec, leg string, started time.Time) []pointerEvent {
	var events []pointerEvent
	for _, a := range actions {
		moves, rings := pointerAction(a.Action, a.Args)
		if !moves || cmp.Or(a.Leg, "head") != leg {
			continue
		}
		at, point := a.SentAt, a.Click
		if at.IsZero() {
			// Refusals and old logs have no dispatch time. Hide at the logged
			// time instead of pretending the receipt time was the action time.
			at, point = a.At, nil
		}
		if at.IsZero() {
			continue
		}
		offset := at.Sub(started)
		if a.RecordOffsetMS != nil {
			offset = time.Duration(*a.RecordOffsetMS) * time.Millisecond
		}
		if offset < 0 {
			continue
		}
		if !validPointer(point) {
			point = nil
		}
		events = append(events, pointerEvent{offset.Truncate(time.Millisecond), point, rings})
	}
	slices.SortStableFunc(events, func(a, b pointerEvent) int { return cmp.Compare(a.at, b.at) })
	return events
}

// pointerAt is a pure timeline lookup. Positions are interpolated in viewport
// fractions, then scaled to this JPEG's actual dimensions, before video padding.
func pointerAt(events []pointerEvent, at time.Duration, size image.Point) pointerState {
	i, _ := slices.BinarySearchFunc(events, at, func(e pointerEvent, at time.Duration) int {
		return cmp.Compare(e.at, at)
	})
	for i < len(events) && events[i].at <= at {
		i++
	}
	i--
	if i < 0 || events[i].point == nil {
		return pointerState{}
	}
	e := events[i]
	x, y := e.point.X/e.point.W, e.point.Y/e.point.H
	if i+1 < len(events) && events[i+1].point != nil {
		next := events[i+1]
		from := max(e.at, next.at-pointerGlide)
		if at >= from && next.at > from {
			progress := float64(at-from) / float64(next.at-from)
			x += (next.point.X/next.point.W - x) * progress
			y += (next.point.Y/next.point.H - y) * progress
		}
	}
	state := pointerState{visible: true, x: x * float64(size.X), y: y * float64(size.Y)}
	for j := i; j >= 0 && at-events[j].at < pointerPulse; j-- {
		e := events[j]
		if e.point == nil {
			break
		}
		if e.rings > 0 {
			state.rings = append(state.rings, pointerRing{e.point.X / e.point.W * float64(size.X), e.point.Y / e.point.H * float64(size.Y), e.rings, float64(at-e.at) / float64(pointerPulse)})
		}
	}
	return state
}

// pointerFrameTimes adds only animation windows to the sparse capture timeline.
// The raw image still holds through idle/reconnect gaps, while the cursor moves.
func pointerFrameTimes(frames []screencastFrame, meta screencastManifest, events []pointerEvent) []time.Duration {
	length := recordingLength(meta)
	times := []time.Duration{0}
	add := func(at time.Duration) {
		if at >= 0 && at < length {
			times = append(times, at)
		}
	}
	for _, f := range frames {
		add(time.Duration(frameOffset(f, meta)) * time.Millisecond)
	}
	for i, e := range events {
		add(e.at)
		from, to := e.at, e.at
		if e.point != nil {
			if i > 0 && events[i-1].point != nil {
				from = max(events[i-1].at, e.at-pointerGlide)
			}
			if e.rings > 0 {
				to += pointerPulse
			}
		}
		for at := max(from, 0); at <= to && at < length; at += pointerFrame {
			add(at)
		}
		add(to)
	}
	slices.Sort(times)
	return slices.Compact(times)
}

// readPointerActions reports corrupt logs instead of silently drawing a partial
// timeline. No action log is normal for a recording with no driven actions.
func readPointerActions(runDir string) ([]actionRec, error) {
	f, err := os.Open(filepath.Join(runDir, "actions.jsonl"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var actions []actionRec
	for dec := json.NewDecoder(f); ; {
		var a actionRec
		if err := dec.Decode(&a); err != nil {
			if errors.Is(err, io.EOF) {
				return actions, nil
			}
			return nil, fmt.Errorf("read cursor actions: %w", err)
		}
		actions = append(actions, a)
	}
}

// drawPointerFrames never writes into the capture directory. The caller owns
// cleanup of the returned directory, including when an encode fails.
func drawPointerFrames(ctx context.Context, dir, leg string, frames []screencastFrame, meta screencastManifest, actions []actionRec) (copies string, drawn []screencastFrame, err error) {
	events := pointerEvents(actions, leg, meta.StartedWall)
	if !slices.ContainsFunc(events, func(e pointerEvent) bool { return e.point != nil }) {
		return "", nil, nil
	}
	copies, err = os.MkdirTemp(dir, ".cursor-")
	if err != nil {
		return "", nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(copies))
			copies, drawn = "", nil
		}
	}()
	if err = os.Mkdir(filepath.Join(copies, "frames"), 0o700); err != nil {
		return copies, nil, err
	}
	var source []byte
	var decoded image.Image
	current := -1
	for _, at := range pointerFrameTimes(frames, meta, events) {
		if err := ctx.Err(); err != nil {
			return copies, nil, err
		}
		i := max(0, current)
		for i+1 < len(frames) && time.Duration(frameOffset(frames[i+1], meta))*time.Millisecond <= at {
			i++
		}
		if i != current {
			source, err = os.ReadFile(filepath.Join(dir, "frames", fmt.Sprintf("%06d.jpg", frames[i].N)))
			if err != nil {
				return copies, nil, err
			}
			decoded, err = jpeg.Decode(bytes.NewReader(source))
			if err != nil {
				return copies, nil, fmt.Errorf("decode cursor frame %d: %w", frames[i].N, err)
			}
			current = i
		}
		data := source
		state := pointerAt(events, at, decoded.Bounds().Size())
		if state.visible {
			img := image.NewRGBA(decoded.Bounds())
			draw.Draw(img, img.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
			drawPointer(img, state)
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
				return copies, nil, err
			}
			data = buf.Bytes()
		}
		n := len(drawn) + 1
		if err := os.WriteFile(filepath.Join(copies, "frames", fmt.Sprintf("%06d.jpg", n)), data, 0o600); err != nil {
			return copies, nil, err
		}
		ms := at.Milliseconds()
		drawn = append(drawn, screencastFrame{N: n, WallMS: meta.StartedWall.Add(at).UnixMilli(), OffsetMS: &ms})
	}
	return copies, drawn, nil
}

func drawPointer(img *image.RGBA, state pointerState) {
	for _, r := range state.rings {
		for i := range r.count {
			radius := 10 + 12*r.progress + float64(i)*7
			paintPointer(img, r.x-radius-3, r.y-radius-3, r.x+radius+3, r.y+radius+3, func(x, y float64) color.RGBA {
				d := math.Abs(math.Hypot(x-r.x, y-r.y) - radius)
				alpha := uint8(255 * (1 - r.progress))
				if d <= 1 {
					return color.RGBA{alpha, alpha, alpha, alpha}
				}
				if d <= 2 {
					return color.RGBA{0, 0, 0, alpha}
				}
				return color.RGBA{}
			})
		}
	}
	// Arrow tip is the action point. A light border surrounds the dark fill.
	polygon := [][2]float64{{0, 0}, {0, 23}, {6, 18}, {11, 29}, {16, 27}, {11, 16}, {20, 16}}
	paintPointer(img, state.x-2, state.y-2, state.x+22, state.y+31, func(x, y float64) color.RGBA {
		x, y = x-state.x, y-state.y
		inside, distance := false, math.Inf(1)
		for i, a := range polygon {
			b := polygon[(i+1)%len(polygon)]
			if (a[1] > y) != (b[1] > y) && x < (b[0]-a[0])*(y-a[1])/(b[1]-a[1])+a[0] {
				inside = !inside
			}
			dx, dy := b[0]-a[0], b[1]-a[1]
			u := min(1, max(0, ((x-a[0])*dx+(y-a[1])*dy)/(dx*dx+dy*dy)))
			distance = min(distance, math.Hypot(x-a[0]-u*dx, y-a[1]-u*dy))
		}
		if distance <= 1.25 {
			return color.RGBA{250, 250, 250, 255}
		}
		if inside {
			return color.RGBA{25, 25, 25, 255}
		}
		return color.RGBA{}
	})
}

// Supersample only the small cursor/ring bounds, using premultiplied alpha.
func paintPointer(img *image.RGBA, x0, y0, x1, y1 float64, sample func(float64, float64) color.RGBA) {
	bounds := image.Rect(int(math.Floor(x0)), int(math.Floor(y0)), int(math.Ceil(x1)), int(math.Ceil(y1))).Intersect(img.Bounds())
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			var r, g, b, a uint32
			for sy := range 4 {
				for sx := range 4 {
					c := sample(float64(x)+(float64(sx)+0.5)/4, float64(y)+(float64(sy)+0.5)/4)
					r, g, b, a = r+uint32(c.R), g+uint32(c.G), b+uint32(c.B), a+uint32(c.A)
				}
			}
			r, g, b, a = r/16, g/16, b/16, a/16
			old := img.RGBAAt(x, y)
			img.SetRGBA(x, y, color.RGBA{uint8(r + uint32(old.R)*(255-a)/255), uint8(g + uint32(old.G)*(255-a)/255), uint8(b + uint32(old.B)*(255-a)/255), 255})
		}
	}
}
