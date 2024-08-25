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

var cycle int
var mouse_pressed = make(map[ebiten.MouseButton]int)
var mouse_released = make(map[ebiten.MouseButton]int)
var input_mu sync.Mutex

func mouse_just_pressed(button ebiten.MouseButton) bool {
	input_mu.Lock()
	defer input_mu.Unlock()
	return mouse_pressed[button] == cycle
}

func mouse_just_released(button ebiten.MouseButton) bool {
	input_mu.Lock()
	defer input_mu.Unlock()
	return mouse_released[button] == cycle
}

type layout_t interface {
	layout(*ebiten.Image) *ebiten.Image
}

type grid_layout struct {
	columns int
	rows    int
	current int
}

func (l *grid_layout) layout(src *ebiten.Image) (dst *ebiten.Image) {
	if l.current == l.rows*l.columns {
		return nil
	}
	col := l.current % l.columns
	row := l.current / l.columns
	l.current++
	bounds := src.Bounds()
	cell_width := bounds.Dx() / l.columns
	cell_height := bounds.Dy() / l.rows
	x := bounds.Min.X + (col * cell_width)
	y := bounds.Min.Y + (row * cell_height)
	return src.SubImage(image.Rect(x, y, x+cell_width, y+cell_height)).(*ebiten.Image)
}

type game struct{}

type uid_t struct {
	base uint64
	id   uint64
}

var uid_zero uid_t

type ui_context_t struct {
	layers              []*ebiten.Image
	layout              layout_t
	triggers            map[uid_t]trigger_t
	uid_base_occurences map[uintptr]uint64
	uid_cycle           map[uid_t]int
	frame_triggers      []trigger_t
	hover_trigger_uid   uid_t
	press_trigger_uid   uid_t
}

func new_ui_context() *ui_context_t {
	return &ui_context_t{
		triggers:            make(map[uid_t]trigger_t),
		uid_base_occurences: make(map[uintptr]uint64),
		uid_cycle:           make(map[uid_t]int),
	}
}

func (ctx *ui_context_t) start_frame(dst *ebiten.Image) {
	clear(ctx.layers)
	ctx.layers = append(ctx.layers[:0], dst)
}

func (ctx *ui_context_t) end_frame() {
	clear(ctx.uid_base_occurences)
}

func (ctx *ui_context_t) set_layout(layout layout_t) {
	ctx.layout = layout
}

func (ctx *ui_context_t) push(x, y, w, h int, layout layout_t) {
	if len(ctx.layers) == 0 {
		panic("ui context not initialized")
	}
	top := ctx.layers[len(ctx.layers)-1]
	ctx.layers = append(ctx.layers, top.SubImage(image.Rect(x, y, x+w, y+h)).(*ebiten.Image))
	ctx.layout = layout
}

func (ctx *ui_context_t) pop() {
	if len(ctx.layers) > 0 {
		ctx.layers[len(ctx.layers)-1] = nil
		ctx.layers = ctx.layers[:len(ctx.layers)-1]
	}
	ctx.layout = nil
}

func (ctx *ui_context_t) next() *ebiten.Image {
	if len(ctx.layers) == 0 {
		panic("ui context not initialized")
	}
	top := ctx.layers[len(ctx.layers)-1]
	if l := ctx.layout; l != nil {
		return l.layout(top)
	}
	return top
}

type trigger_t struct {
	button_behavior_t
	uid    uid_t
	bounds image.Rectangle
}

type button_mode int

const (
	button_mode_activate_on_click_release button_mode = iota
	button_mode_activate_on_click
	button_mode_activate_on_release
	button_mode_activate_on_double_click
)

type button_behavior_t struct {
	mode        button_mode
	on_enter    func(x, y int)
	on_press    func(btn ebiten.MouseButton)
	on_exit     func(x, y int)
	on_activate func()
	on_release  func(btn ebiten.MouseButton)
}

type button_t struct {
	behavior button_behavior_t
	text     string
}

func (ctx *ui_context_t) uid(skip int) (uid uid_t) {
	var rpc [1]uintptr
	runtime.Callers(skip, rpc[:])
	frame, _ := runtime.CallersFrames(rpc[:]).Next()
	pc := frame.PC
	uid = uid_t{
		base: uint64(pc),
		id:   ctx.uid_base_occurences[pc],
	}
	ctx.uid_cycle[uid] = cycle
	ctx.uid_base_occurences[pc]++
	return
}

func (ctx *ui_context_t) button(button button_t) {
	uid := ctx.uid(3)
	dst := ctx.next()
	if ctx.press_trigger_uid == uid {
		dst.Fill(color.RGBA{60, 60, 60, 255})
	} else if ctx.hover_trigger_uid == uid {
		dst.Fill(color.RGBA{128, 128, 128, 255})
	} else {
		dst.Fill(color.RGBA{80, 80, 80, 255})
	}
	draw_border(dst, 1, 1, color.RGBA{127, 127, 127, 255})
	draw_border(dst, 0, 1, color.RGBA{196, 196, 196, 255})
	if button.text != "" {
		phase := float64(elapsed()) / 1e9
		x := float32(0.5 + math.Cos(phase)/2)
		y := float32(0.5 + math.Sin(phase)/2)
		draw_string(dst, button.text, x, y)
	}
	ctx.frame_triggers = append(ctx.frame_triggers, trigger_t{
		button_behavior_t: button.behavior,
		uid:               uid,
		bounds:            dst.Bounds(),
	})
	return
}

func (g *game) Update() error {
	input_mu.Lock()
	defer input_mu.Unlock()

	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		mouse_pressed[ebiten.MouseButtonLeft] = cycle
	}
	if inpututil.IsMouseButtonJustReleased(ebiten.MouseButtonLeft) {
		mouse_released[ebiten.MouseButtonLeft] = cycle
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
			ctx.button(button_t{
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
				text: fmt.Sprintf("%d,%d", i, j),
			})

		}
	}

	ctx.end_frame()

	var hover_trigger trigger_t
	var cursor_over_trigger bool
	for _, trigger := range ctx.frame_triggers {
		if cursor_within(trigger.bounds) {
			hover_trigger = trigger
			cursor_over_trigger = true
		}
	}
	ctx.frame_triggers = ctx.frame_triggers[:0]

	if uid := hover_trigger.uid; uid != ctx.hover_trigger_uid {
		var prev trigger_t

		if ctx.hover_trigger_uid != uid_zero {
			prev = ctx.triggers[ctx.hover_trigger_uid]
		}

		next, ok := ctx.triggers[uid]

		if !ok {
			ctx.triggers[uid] = hover_trigger
			next = hover_trigger
		}

		cx, cy := ebiten.CursorPosition()

		log.Printf("%+v -> %+v", prev.uid, next.uid)

		if on_exit := prev.on_exit; on_exit != nil {
			on_exit(cx, cy)
		}

		if on_enter := next.on_enter; on_enter != nil {
			on_enter(cx, cy)
		}

		ctx.hover_trigger_uid = uid
	}

	if cursor_over_trigger {
		trigger := ctx.triggers[hover_trigger.uid]

		if mouse_just_pressed(ebiten.MouseButtonLeft) {
			if on_press := trigger.on_press; on_press != nil {
				on_press(ebiten.MouseButtonLeft)
			}

			if trigger.mode == button_mode_activate_on_click {
				if on_activate := trigger.on_activate; on_activate != nil {
					on_activate()
				}
			}

			ctx.press_trigger_uid = hover_trigger.uid
		}
	}

	if mouse_just_released(ebiten.MouseButtonLeft) {
		if uid := ctx.press_trigger_uid; uid != uid_zero {
			trigger := ctx.triggers[uid]

			if on_release := trigger.on_release; on_release != nil {
				on_release(ebiten.MouseButtonLeft)
			}

			if trigger.mode == button_mode_activate_on_release ||
				trigger.mode == button_mode_activate_on_click_release && cursor_within(trigger.bounds) {
				if on_activate := trigger.on_activate; on_activate != nil {
					on_activate()
				}
			}
			ctx.press_trigger_uid = uid_zero
		}
	}

	cycle++
}

func (g *game) Layout(outer_width, outer_height int) (width, height int) {
	return 800, 600
}
