package main

import (
	"bytes"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
	"log"
	"math"
	"os"
	"runtime"
	"runtime/pprof"
	"slices"
	"time"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
)

const (
	game_width  = 800
	game_height = 600
	game_aspect = float(game_width) / float(game_height)
)

type (
	float = float32
	vec2  = mgl32.Vec2
	vec3  = mgl32.Vec3
	vec4  = mgl32.Vec4
	mat4  = mgl32.Mat4
)

//go:embed wall.obj
var wall_obj []byte

//go:embed diffuse.jpg
var diffuse_jpg []byte
var diffuse *ebiten.Image

var cpu_profile = flag.String("cpuprofile", "", "write cpu profile to `file`")
var mem_profile = flag.String("memprofile", "", "write memory profile to `file`")

func main() {
	flag.Parse()

	if *cpu_profile != "" {
		f, err := os.Create(*cpu_profile)
		if err != nil {
			log.Fatal("could not create CPU profile: ", err)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal("could not start CPU profile: ", err)
		}
		defer pprof.StopCPUProfile()
	}

	if *mem_profile != "" {
		defer func() {
			f, err := os.Create(*mem_profile)
			if err != nil {
				log.Fatal("could not create memory profile:", err)
			}
			defer f.Close()
			runtime.GC() // get up-to-date statistics
			if err := pprof.WriteHeapProfile(f); err != nil {
				log.Fatal("could not write memory profile:", err)
			}
		}()
	}

	mesh, err := load_obj(wall_obj)

	if err != nil {
		panic(err)
	}

	image, _, err := image.Decode(bytes.NewReader(diffuse_jpg))

	if err != nil {
		panic(err)
	}

	game := &game{
		texture: ebiten.NewImageFromImage(image),
		mesh:    mesh,
		camera: camera{
			yaw: math.Pi,
			pos: vec3{0, 10, -10},
		},
	}

	ebiten.SetWindowTitle("001-textures")
	ebiten.SetWindowSize(game_width, game_height)
	ebiten.SetVsyncEnabled(false)

	err = ebiten.RunGame(game)

	if err != nil {
		panic(err)
	}
}

type game struct {
	cycle     float32
	texture   *ebiten.Image
	mesh      *mesh
	frametime time.Duration
	camera    camera
}

type camera struct {
	pitch float
	yaw   float
	pos   vec3

	drag_x   int
	drag_y   int
	dragging bool

	up      vec3
	forward vec3
	right   vec3

	view_matrix mat4
}

type triangle struct {
	p1, p2, p3 uint16
	t1, t2, t3 uint16
}

type mesh struct {
	triangles []triangle
	points    []vec3
	texcoords []vec2
}

type vertex struct {
	position vec4
	texcoord vec2
}

func interpolate_vec4(v1, v2, v3 vec4, f vec3) (result vec4) {
	result = result.Add(v1.Mul(f.X()))
	result = result.Add(v2.Mul(f.Y()))
	result = result.Add(v3.Mul(f.Z()))
	return
}

func interpolate_vec2(v1, v2, v3 vec2, f vec3) (result vec2) {
	result = result.Add(v1.Mul(f.X()))
	result = result.Add(v2.Mul(f.Y()))
	result = result.Add(v3.Mul(f.Z()))
	return
}

func interpolate_vertex(v1, v2, v3 vertex, f vec3) (result vertex) {
	result.position = interpolate_vec4(v1.position, v2.position, v3.position, f)
	result.texcoord = interpolate_vec2(v1.texcoord, v2.texcoord, v3.texcoord, f)
	return
}

func load_obj(src []byte) (*mesh, error) {
	reader := bytes.NewReader(src)
	mesh := &mesh{}
	for {
		var typ string
		if _, err := fmt.Fscan(reader, &typ); err != nil {
			if errors.Is(io.EOF, err) {
				break
			}
			return nil, fmt.Errorf("bad type: %w", err)
		}
		switch typ {
		default:
			return nil, fmt.Errorf("unknown type: %s", typ)
		case "#", "o", "s", "l":
			fmt.Fscanln(reader)
		case "v":
			var x, y, z float
			if _, err := fmt.Fscanf(reader, "%f %f %f", &x, &y, &z); err != nil {
				return nil, fmt.Errorf("bad vertex: %w", err)
			}
			mesh.points = append(mesh.points, vec3{x, y, z})
		case "vt":
			var s, t float
			if _, err := fmt.Fscanf(reader, "%f %f", &s, &t); err != nil {
				return nil, fmt.Errorf("bad texcoord: %w", err)
			}
			mesh.texcoords = append(mesh.texcoords, vec2{s, t})
		case "f":
			var v1, v2, v3 uint16
			var t1, t2, t3 uint16
			if _, err := fmt.Fscanf(reader, "%d/%d %d/%d %d/%d", &v1, &t1, &v2, &t2, &v3, &t3); err != nil {
				return nil, fmt.Errorf("bad face: %w", err)
			}
			mesh.triangles = append(mesh.triangles, triangle{
				p1: v1 - 1,
				p2: v2 - 1,
				p3: v3 - 1,
				t1: t1 - 1,
				t2: t2 - 1,
				t3: t3 - 1,
			})
		}
	}
	return mesh, nil
}

type viewport struct {
	x   int
	y   int
	w   int
	h   int
	w_2 int
	h_2 int
}

func (self *game) Layout(outerWidth, outerHeight int) (int, int) {
	return game_width, game_height
}

func (self *game) Update() error {
	self.cycle++

	if ebiten.IsMouseButtonPressed(ebiten.MouseButtonLeft) {
		cx, cy := ebiten.CursorPosition()

		// doing the logic in the next update ensures we don't get some crazy snapping
		if !self.camera.dragging {
			self.camera.dragging = true
		} else {
			dx := float(cx-self.camera.drag_x) / 100.0
			dy := float(cy-self.camera.drag_y) / 100.0

			self.camera.pitch = mgl32.Clamp(self.camera.pitch+dy, -math.Pi/2, math.Pi/2)
			self.camera.yaw -= dx

			view := mgl32.Ident4()
			view = view.Mul4(mgl32.HomogRotate3DX(self.camera.pitch))
			view = view.Mul4(mgl32.HomogRotate3DY(self.camera.yaw))

			self.camera.right = view.Row(0).Vec3().Mul(-1)
			self.camera.up = view.Row(1).Vec3()
			self.camera.forward = view.Row(2).Vec3().Mul(-1)

			if ebiten.IsKeyPressed(ebiten.KeyW) || ebiten.IsKeyPressed(ebiten.KeyUp) {
				self.camera.pos = self.camera.pos.Add(self.camera.forward)
			} else if ebiten.IsKeyPressed(ebiten.KeyS) || ebiten.IsKeyPressed(ebiten.KeyDown) {
				self.camera.pos = self.camera.pos.Sub(self.camera.forward)
			}

			if ebiten.IsKeyPressed(ebiten.KeyD) || ebiten.IsKeyPressed(ebiten.KeyRight) {
				self.camera.pos = self.camera.pos.Add(self.camera.right)
			} else if ebiten.IsKeyPressed(ebiten.KeyA) || ebiten.IsKeyPressed(ebiten.KeyLeft) {
				self.camera.pos = self.camera.pos.Sub(self.camera.right)
			}

			self.camera.view_matrix = view.Mul4(mgl32.Translate3D(
				-self.camera.pos.X(),
				-self.camera.pos.Y(),
				-self.camera.pos.Z(),
			))
		}

		self.camera.drag_x = cx
		self.camera.drag_y = cy
	} else {
		self.camera.dragging = false
	}

	return nil
}

type plane struct {
	origin vec4
	normal vec4
}

// test determines if `v` is in front of the plane.
func (p plane) test(v vec4) bool {
	return v.Sub(p.origin).Dot(p.normal) > 0
}

// intersection returns the point of contact of a line segment between a->b to our plane.
func (p plane) intersection(a, b vec4) vec4 {
	u := b.Sub(a)
	w := a.Sub(p.origin)
	d := p.normal.Dot(u)
	n := -p.normal.Dot(w)
	return a.Add(u.Mul(n / d))
}

var clip_planes = [...]plane{
	{origin: vec4{1, 0, 0, 1}, normal: vec4{-1, 0, 0, 1}}, // right
	{origin: vec4{-1, 0, 0, 1}, normal: vec4{1, 0, 0, 1}}, // left
	{origin: vec4{0, 1, 0, 1}, normal: vec4{0, -1, 0, 1}}, // bottom
	{origin: vec4{0, -1, 0, 1}, normal: vec4{0, 1, 0, 1}}, // top
	{origin: vec4{0, 0, 1, 1}, normal: vec4{0, 0, -1, 1}}, // front
	{origin: vec4{0, 0, -1, 1}, normal: vec4{0, 0, 1, 1}}, // back
}

// https://en.wikipedia.org/wiki/Sutherland-Hodgman_algorithm
func sutherland_hodgman_3d(p1, p2, p3 vec4) []vec4 {
	output := []vec4{p1, p2, p3}
	for _, plane := range clip_planes {
		input := output
		output = nil
		if len(input) == 0 {
			return nil
		}
		s := input[len(input)-1]
		for _, e := range input {
			if plane.test(e) {
				if !plane.test(s) {
					x := plane.intersection(s, e)
					output = append(output, x)
				}
				output = append(output, e)
			} else if plane.test(s) {
				x := plane.intersection(s, e)
				output = append(output, x)
			}
			s = e
		}
	}
	return output
}

// https://en.wikipedia.org/wiki/Barycentric_coordinate_system
func barycentric(p1, p2, p3, p vec3) vec3 {
	v0 := p2.Sub(p1)
	v1 := p3.Sub(p1)
	v2 := p.Sub(p1)
	d00 := v0.Dot(v0)
	d01 := v0.Dot(v1)
	d11 := v1.Dot(v1)
	d20 := v2.Dot(v0)
	d21 := v2.Dot(v1)
	d := d00*d11 - d01*d01
	v := (d11*d20 - d01*d21) / d
	w := (d00*d21 - d01*d20) / d
	u := 1 - v - w
	return vec3{u, v, w}
}

type context struct {
	view_matrix mat4
	proj_matrix mat4

	// viewport is used to convert normalized device coordinates to screen coordinates
	viewport viewport

	// statistics
	drawn_triangles int

	// the following are not required to be stored here,
	// they serve as buffers to reduce overall allocations.

	clip_space_points []vec4
	screen_triangles  []screen_triangle
	vertices          []ebiten.Vertex
	indices           []uint16
}

type screen_triangle struct {
	v1, v2, v3 vertex
	distance   float
}

func (c *context) set_viewport(x, y, w, h int) {
	c.viewport.x = x
	c.viewport.y = y
	c.viewport.w = w
	c.viewport.h = h
	c.viewport.w_2 = w / 2
	c.viewport.h_2 = h / 2
}

func clip_out_of_bounds(a vec4) bool {
	x, y, z, w := a.X(), a.Y(), a.Z(), a.W()
	return x < -w || x > w || y < -w || y > w || z < -w || z > w
}

func (c *context) clip_to_ndc(src vec4) vec4 {
	return src.Mul(1.0 / src.W())
}

func (c *context) ndc_to_screen(src vec4) vec4 {
	w_2 := float(c.viewport.w_2)
	h_2 := float(c.viewport.h_2)
	z := 0.5*src.Z() + 0.5
	return vec4{
		w_2*src.X() + w_2,
		h_2*src.Y() + h_2,
		z,
		1.0 / z,
	}
}

func (ctx *context) push_mesh(mesh *mesh) {
	// save us some calculations by doing this here instead of per point
	projection_view_matrix := ctx.proj_matrix.Mul4(ctx.view_matrix)

	// transform all the mesh points into clip space
	for _, point := range mesh.points {
		point := projection_view_matrix.Mul4x1(point.Vec4(1))
		ctx.clip_space_points = append(ctx.clip_space_points, point)
	}

	for _, triangle := range mesh.triangles {
		v1 := vertex{
			position: ctx.clip_space_points[triangle.p1],
			texcoord: mesh.texcoords[triangle.t1],
		}
		v2 := vertex{
			position: ctx.clip_space_points[triangle.p2],
			texcoord: mesh.texcoords[triangle.t2],
		}
		v3 := vertex{
			position: ctx.clip_space_points[triangle.p3],
			texcoord: mesh.texcoords[triangle.t3],
		}

		if clip_out_of_bounds(v1.position) || clip_out_of_bounds(v2.position) || clip_out_of_bounds(v3.position) {
			ctx.clip_triangle_and_push(v1, v2, v3)
		} else {
			ctx.push_triangle(v1, v2, v3)
		}
	}
}

func (c *context) clip_triangle_and_push(v1, v2, v3 vertex) {
	points := sutherland_hodgman_3d(v1.position, v2.position, v3.position)

	p1 := v1.position.Vec3()
	p2 := v2.position.Vec3()
	p3 := v3.position.Vec3()

	for i := 2; i < len(points); i++ {
		b1 := barycentric(p1, p2, p3, points[0].Vec3())
		b2 := barycentric(p1, p2, p3, points[i-1].Vec3())
		b3 := barycentric(p1, p2, p3, points[i].Vec3())

		c.push_triangle(
			interpolate_vertex(v1, v2, v3, b1),
			interpolate_vertex(v1, v2, v3, b2),
			interpolate_vertex(v1, v2, v3, b3),
		)
	}
}

func (c *context) push_triangle(v1, v2, v3 vertex) {
	ndc1 := c.clip_to_ndc(v1.position)
	ndc2 := c.clip_to_ndc(v2.position)
	ndc3 := c.clip_to_ndc(v3.position)

	// back-face culling
	if (ndc2.X()-ndc1.X())*(ndc3.Y()-ndc1.Y())-(ndc3.X()-ndc1.X())*(ndc2.Y()-ndc1.Y()) <= 0 {
		return
	}

	v1.position = c.ndc_to_screen(ndc1)
	v2.position = c.ndc_to_screen(ndc2)
	v3.position = c.ndc_to_screen(ndc3)

	c.screen_triangles = append(c.screen_triangles, screen_triangle{
		v1:       v1,
		v2:       v2,
		v3:       v3,
		distance: (v1.position.Z() + v2.position.Z() + v3.position.Z()) / 3,
	})
}

func (ctx *context) sort_triangles() {
	slices.SortFunc(ctx.screen_triangles, func(a, b screen_triangle) int {
		if a.distance >= b.distance {
			return -1
		}
		return 1
	})
}

func (ctx *context) draw_triangles(texture, target *ebiten.Image) {

	tex_width := float(texture.Bounds().Dx())
	tex_height := float(texture.Bounds().Dy())

	for _, triangle := range ctx.screen_triangles {
		v1 := triangle.v1
		v2 := triangle.v2
		v3 := triangle.v3

		ctx.vertices = append(ctx.vertices,
			ebiten.Vertex{
				SrcX:   v1.texcoord.X() * tex_width,
				SrcY:   v1.texcoord.Y() * tex_height,
				DstX:   v1.position.X(),
				DstY:   v1.position.Y(),
				ColorR: 1,
				ColorG: 1,
				ColorB: 1,
				ColorA: 1,
			},
			ebiten.Vertex{
				SrcX:   v2.texcoord.X() * tex_width,
				SrcY:   v2.texcoord.Y() * tex_height,
				DstX:   v2.position.X(),
				DstY:   v2.position.Y(),
				ColorR: 1,
				ColorG: 1,
				ColorB: 1,
				ColorA: 1,
			},
			ebiten.Vertex{
				SrcX:   v3.texcoord.X() * tex_width,
				SrcY:   v3.texcoord.Y() * tex_height,
				DstX:   v3.position.X(),
				DstY:   v3.position.Y(),
				ColorR: 1,
				ColorG: 1,
				ColorB: 1,
				ColorA: 1,
			},
		)

		first_index := uint16(len(ctx.indices))
		ctx.indices = append(ctx.indices, first_index, first_index+1, first_index+2)
	}

	target.DrawTriangles(ctx.vertices, ctx.indices, texture, &ebiten.DrawTrianglesOptions{
		AntiAlias: true,
	})

	ctx.drawn_triangles = len(ctx.indices) / 3

	// reset buffers
	ctx.clip_space_points = ctx.clip_space_points[:0]
	ctx.screen_triangles = ctx.screen_triangles[:0]
	ctx.vertices = ctx.vertices[:0]
	ctx.indices = ctx.indices[:0]
}

func (self *game) Draw(screen *ebiten.Image) {
	defer func(t time.Time) {
		ft := time.Now().Sub(t)
		if self.frametime == 0 {
			self.frametime = ft
		} else {
			self.frametime += (ft - self.frametime) / 2
		}
	}(time.Now())

	var ctx context
	w := screen.Bounds().Dx()
	h := screen.Bounds().Dy()

	ctx.set_viewport(0, 0, w, h)

	// If you use orthographic then the Z axis will invert for everything.
	// https://www.songho.ca/opengl/gl_projectionmatrix.html#perspective
	// ctx.set_orthographic(-eye_distance*game_aspect, eye_distance*game_aspect, eye_distance, -eye_distance, 0.1, 10)

	ctx.proj_matrix = mgl32.Perspective(30, game_aspect, 0.1, 100)

	// the camera view matrix is invalid until the user controls it
	if self.camera.view_matrix.Det() == 0 {
		ctx.view_matrix = mgl32.LookAtV(
			vec3{0, 10, -10},
			vec3{0, 10, 10},
			vec3{0, 1, 0},
		)
	} else {
		ctx.view_matrix = self.camera.view_matrix
	}

	ctx.push_mesh(self.mesh)
	ctx.sort_triangles()
	ctx.draw_triangles(self.texture, screen)

	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("TPS: %.0f", ebiten.ActualTPS()), 0, 0)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("FPS: %.0f (%v)", ebiten.ActualFPS(), self.frametime), 0, 14)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Triangles: %d", ctx.drawn_triangles), 0, 28)
}
