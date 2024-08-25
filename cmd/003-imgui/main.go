package main

import (
	"fmt"
	"image"
	"image/color"
	"log"
	"math"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

func cursor_within(rect image.Rectangle) bool {
	cx, cy := ebiten.CursorPosition()
	return cx >= rect.Min.X && cy >= rect.Min.Y && cx < rect.Max.X && cy < rect.Max.Y
}

func draw_border(dst *ebiten.Image, inset, width float32, clr color.Color) {
	bounds := dst.Bounds()
	inset += width / 2
	x := float32(bounds.Min.X) + inset
	y := float32(bounds.Min.Y) + inset
	w := float32(bounds.Dx()) - inset*2
	h := float32(bounds.Dy()) - inset*2
	vector.StrokeRect(dst, x, y, w, h, width, clr, false)
}

func draw_string(dst *ebiten.Image, s string, align_x, align_y float32) {
	bounds := dst.Bounds()
	x := float32(bounds.Min.X)
	y := float32(bounds.Min.Y)
	width := float32(bounds.Dx())
	height := float32(bounds.Dy())

	const font_height = 16

	n_lines := strings.Count(s, "\n") + 1
	text_height := float32(n_lines * font_height)
	y += (height - text_height) * align_y

	for _, line := range strings.Split(s, "\n") {
		const char_width = 6
		line_width := float32(len(line) * char_width)
		x := x + (width-line_width)*align_x
		ebitenutil.DebugPrintAt(dst, line, int(x), int(y))
		y += font_height
	}
}

func main() {
	ebiten.SetWindowSize(800, 600)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeDisabled)
	ebiten.SetTPS(60)
	ebiten.SetVsyncEnabled(true)

	if err := ebiten.RunGame(&game{}); err != nil {
		log.Panic(err)
	}
}

var time_start = time.Now()

func elapsed() time.Duration {
	return time_start.Sub(time.Now())
}

var current_frame int

// we need input state synchronized with the frame due to checking inputs at the end of a frame
var (
	input_mu       sync.Mutex
	mouse_pressed  = make(map[ebiten.MouseButton]int)
	mouse_released = make(map[ebiten.MouseButton]int)
)

func mouse_just_pressed(button ebiten.MouseButton) bool {
	input_mu.Lock()
	defer input_mu.Unlock()
	return mouse_pressed[button] == current_frame
}

func mouse_just_released(button ebiten.MouseButton) bool {
	input_mu.Lock()
	defer input_mu.Unlock()
	return mouse_released[button] == current_frame
}

type layout_t interface {
	layout(src image.Rectangle) (dst image.Rectangle)
}

// grid_layout is a rough example of how layouts might be implemented
type grid_layout struct {
	columns int
	rows    int
	current int
}

func (l *grid_layout) layout(src image.Rectangle) (dst image.Rectangle) {
	if l.current == l.rows*l.columns {
		return image.Rectangle{}
	}
	col := l.current % l.columns
	row := l.current / l.columns
	l.current++
	cell_width := src.Dx() / l.columns
	cell_height := src.Dy() / l.rows
	x := src.Min.X + (col * cell_width)
	y := src.Min.Y + (row * cell_height)
	w := cell_width
	h := cell_height
	return image.Rect(x, y, x+w, y+h)
}

type game struct{}

// uid_t is a unique identifier that should remain consistent between frames.
type uid_t struct {
	// base is typically derived from a program counter, however it can be any deterministic value that stays the same between frames
	base uint64
	// id is typically a value starting at 0 and incrementing for each time a `base` is reused.
	id uint64
}

var uid_zero uid_t

type ui_context_t struct {
	// layers tracks the clipping of the context. The last entry is always the "top" or "active" clipping area.
	layers []*ebiten.Image

	// layout affects the returned *ebiten.Image of ctx.next()
	layout layout_t

	// triggers is a mapping of uid->trigger for behaviors that can happen with a delay...
	// like pressing a button, dragging away, and then releasing
	triggers map[uid_t]trigger_t

	// uid_base_occurences is a mapping of program counters (PC) to the number of occurences on the current frame.
	// This map gets cleared at the end of each frame
	uid_base_occurences map[uintptr]uint64

	// uid_frame is a mapping of UIDs to the cycle
	uid_frame map[uid_t]int

	// frame_triggers is a per-frame tracker of triggers used for testing input against. This list should always be populated
	// by draw-order to ensure the top level trigger is properly detected.
	frame_triggers []trigger_t

	// hover_uid is a global state for which uid is hovered.
	hover_uid uid_t

	// press_uid is the global state for which trigger is pressed. Pressed as in: mouse is currently down, not released.
	press_uid uid_t
}

func new_ui_context() *ui_context_t {
	return &ui_context_t{
		triggers:            make(map[uid_t]trigger_t),
		uid_base_occurences: make(map[uintptr]uint64),
		uid_frame:           make(map[uid_t]int),
	}
}

// start_frame resets and initializes the context with a destination image
func (ctx *ui_context_t) start_frame(dst *ebiten.Image) {
	clear(ctx.layers)                        // we're using 'clear' to avoid holding onto references
	ctx.layers = append(ctx.layers[:0], dst) //
}

// end_frame performs cleanup on per-frame state and runs logic to perform button inputs
func (ctx *ui_context_t) end_frame() {
	clear(ctx.uid_base_occurences)

	var hovered_trigger trigger_t
	var cursor_over_trigger bool

	for _, trigger := range ctx.frame_triggers {
		if cursor_within(trigger.bounds) {
			hovered_trigger = trigger
			cursor_over_trigger = true
		}
	}
	ctx.frame_triggers = ctx.frame_triggers[:0]

	if next_uid := hovered_trigger.uid; next_uid != ctx.hover_uid {
		var prev trigger_t

		if ctx.hover_uid != uid_zero {
			prev = ctx.triggers[ctx.hover_uid]
		}

		next, ok := ctx.triggers[next_uid]

		if !ok {
			ctx.triggers[next_uid] = hovered_trigger
			next = hovered_trigger
		}

		cx, cy := ebiten.CursorPosition()

		log.Printf("%+v -> %+v", prev.uid, next.uid)

		if on_exit := prev.on_exit; on_exit != nil {
			on_exit(cx, cy)
		}

		if on_enter := next.on_enter; on_enter != nil {
			on_enter(cx, cy)
		}

		ctx.hover_uid = next_uid
	}

	if cursor_over_trigger {
		trigger := ctx.triggers[hovered_trigger.uid]

		if mouse_just_pressed(ebiten.MouseButtonLeft) {
			if on_press := trigger.on_press; on_press != nil {
				on_press(ebiten.MouseButtonLeft)
			}

			if trigger.mode == button_mode_activate_on_click {
				if on_activate := trigger.on_activate; on_activate != nil {
					on_activate()
				}
			}

			ctx.press_uid = hovered_trigger.uid
		}
	}

	if mouse_just_released(ebiten.MouseButtonLeft) {
		if trigger := ctx.triggers[ctx.press_uid]; trigger.uid != uid_zero {
			if on_release := trigger.on_release; on_release != nil {
				on_release(ebiten.MouseButtonLeft)
			}

			if trigger.mode == button_mode_activate_on_release ||
				trigger.mode == button_mode_activate_on_click_release && cursor_within(trigger.bounds) {
				if on_activate := trigger.on_activate; on_activate != nil {
					on_activate()
				}
			}
			ctx.press_uid = uid_zero
		}
	}

	ctx.gc()
}

// stale_uid_frames is how many frames need to elapse before a uid is considered 'stale'
const stale_uid_frames = 5

// gc performs garbage collection on this ui context. This may not be entirely necessary
// since the size of a trigger in memory is barely anything at all. However, if there were
// hundreds of thousands then this might make a difference in memory usage over time.
//
// This will become more important when larger state is retained.
func (ctx *ui_context_t) gc() {
	var stale_uids []uid_t
	for uid, frame := range ctx.uid_frame {
		if current_frame-frame >= stale_uid_frames {
			stale_uids = append(stale_uids, uid)
		}
	}
	// delete references to the uid
	for _, uid := range stale_uids {
		delete(ctx.uid_frame, uid)
		delete(ctx.triggers, uid)
	}
}

func (ctx *ui_context_t) set_layout(layout layout_t) {
	ctx.layout = layout
}

// push pushes a subimage of the current image onto the layer stack, effectively making it our new working area.
func (ctx *ui_context_t) push(x, y, w, h int, layout layout_t) {
	if len(ctx.layers) == 0 {
		panic("ui context not initialized")
	}
	top := ctx.layers[len(ctx.layers)-1]
	ctx.layers = append(ctx.layers, top.SubImage(image.Rect(x, y, x+w, y+h)).(*ebiten.Image))
	ctx.layout = layout
}

// push_trigger pushes a per-frame trigger for input for testing at the end of the current frame.
func (ctx *ui_context_t) push_trigger(uid uid_t, bounds image.Rectangle, behavior button_behavior_t) {
	ctx.frame_triggers = append(ctx.frame_triggers, trigger_t{
		button_behavior_t: behavior,
		uid:               uid,
		bounds:            bounds,
	})
}

// pop pops the top subimage off the layer stack.
func (ctx *ui_context_t) pop() {
	if len(ctx.layers) > 0 {
		ctx.layers[len(ctx.layers)-1] = nil
		ctx.layers = ctx.layers[:len(ctx.layers)-1]
	}
	ctx.layout = nil
}

// next returns the working area of our context. If `ctx.layout` is not `nil`, then the next image will be
// determined by that layout. Because this always works in the context of a subimage, a layout can never
// escape the bounds it begins in.
func (ctx *ui_context_t) next() *ebiten.Image {
	if len(ctx.layers) == 0 {
		panic("ui context not initialized")
	}

	top := ctx.layers[len(ctx.layers)-1]

	if l := ctx.layout; l != nil {
		if bounds := l.layout(top.Bounds()); !bounds.Empty() {
			return top.SubImage(bounds).(*ebiten.Image)
		}
	}

	return top
}

type trigger_t struct {
	uid    uid_t
	bounds image.Rectangle
	button_behavior_t
}

type button_mode int

const (
	button_mode_activate_on_click_release button_mode = iota
	button_mode_activate_on_click
	button_mode_activate_on_release
)

type button_behavior_t struct {
	mode        button_mode
	on_enter    func(x, y int)
	on_press    func(btn ebiten.MouseButton)
	on_exit     func(x, y int)
	on_activate func()
	on_release  func(btn ebiten.MouseButton)
}

type button_args struct {
	text     string
	behavior button_behavior_t
}

func (ctx *ui_context_t) uid(skip int) (uid uid_t) {
	var pcs [1]uintptr
	runtime.Callers(2+skip, pcs[:])
	pc := pcs[0]

	uid = uid_t{
		base: uint64(pc),
		id:   ctx.uid_base_occurences[pc],
	}

	ctx.uid_frame[uid] = current_frame
	ctx.uid_base_occurences[pc]++
	return
}

func (ctx *ui_context_t) button(args button_args) {
	uid := ctx.uid(1)
	dst := ctx.next()

	if ctx.press_uid == uid {
		dst.Fill(color.RGBA{60, 60, 60, 255})
	} else if ctx.hover_uid == uid {
		dst.Fill(color.RGBA{128, 128, 128, 255})
	} else {
		dst.Fill(color.RGBA{80, 80, 80, 255})
	}

	draw_border(dst, 1, 1, color.RGBA{127, 127, 127, 255})
	draw_border(dst, 0, 1, color.RGBA{196, 196, 196, 255})

	if args.text != "" {
		phase := float64(elapsed()) / 1e9
		x := float32(0.5 + math.Cos(phase)/2)
		y := float32(0.5 + math.Sin(phase)/2)
		draw_string(dst, args.text, x, y)
	}

	ctx.push_trigger(uid, dst.Bounds(), args.behavior)

	return
}

func (g *game) Update() error {
	input_mu.Lock()
	defer input_mu.Unlock()

	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		mouse_pressed[ebiten.MouseButtonLeft] = current_frame
	}
	if inpututil.IsMouseButtonJustReleased(ebiten.MouseButtonLeft) {
		mouse_released[ebiten.MouseButtonLeft] = current_frame
	}
	return nil
}

var ctx *ui_context_t

func (g *game) Draw(screen *ebiten.Image) {
	if ctx == nil {
		ctx = new_ui_context()
	}

	ctx.start_frame(screen)

	const columns = 16
	const rows = 16

	ctx.set_layout(&grid_layout{
		columns: columns,
		rows:    rows,
	})

	for i := 0; i < columns; i++ {
		for j := 0; j < rows; j++ {
			ctx.button(button_args{
				text: fmt.Sprintf("%d,%d", i, j),
				behavior: button_behavior_t{
					on_enter: func(x, y int) {
					},
					on_exit: func(x, y int) {
					},
					on_activate: func() {
						fmt.Println(">>> activate", i, j)
					},
					on_press: func(button ebiten.MouseButton) {
					},
					on_release: func(button ebiten.MouseButton) {
					},
				},
			})

		}
	}

	ctx.end_frame()

	current_frame++
}

func (g *game) Layout(outer_width, outer_height int) (width, height int) {
	return 800, 600
}
