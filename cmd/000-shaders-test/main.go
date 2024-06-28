package main

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"image/color"
	_ "image/png"
	"io"
	"math"
	"math/rand/v2"
	"slices"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/hajimehoshi/ebiten/v2"
)

const (
	game_width  = 800
	game_height = 600
	game_aspect = float(game_width) / float(game_height)
)

type (
	float = float32
	vec3  = mgl32.Vec3
	vec4  = mgl32.Vec4
	mat4  = mgl32.Mat4
)

var shader_source = `
//kage:unit pixels
package main

func Fragment(dst vec4, src vec2, color vec4) vec4 {
	return color
}
`

//go:embed suzanne.obj
var suzanne_obj []byte

func main() {
	shader, err := ebiten.NewShader([]byte(shader_source))

	if err != nil {
		panic(err)
	}

	mesh, err := load_obj(suzanne_obj)

	if err != nil {
		panic(err)
	}

	for t := range mesh.triangles {
		mesh.triangles[t].rgba = vec4{
			rand.Float32(), rand.Float32(), rand.Float32(), 1.0,
		}
	}

	white := ebiten.NewImage(1, 1)
	white.Fill(color.White)

	game := &Game{
		shader:  shader,
		white:   white,
		suzanne: mesh,
	}

	ebiten.SetWindowTitle("000-shaders-test")
	ebiten.SetWindowSize(game_width, game_height)

	err = ebiten.RunGame(game)

	if err != nil {
		panic(err)
	}
}

type Game struct {
	cycle   float32
	shader  *ebiten.Shader
	white   *ebiten.Image
	suzanne *mesh
}

type vertex struct {
	position vec3
	color    vec3
}

type triangle struct {
	v1, v2, v3 uint16
	rgba       vec4
}

type mesh struct {
	vertices  []vertex
	triangles []triangle
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
		case "#", "o", "s":
			fmt.Fscanln(reader)
		case "v":
			var x, y, z float
			if _, err := fmt.Fscanf(reader, "%f %f %f", &x, &y, &z); err != nil {
				return nil, fmt.Errorf("bad vertex: %w", err)
			}
			mesh.vertices = append(mesh.vertices, vertex{
				position: vec3{x, y, z},
			})
		case "f":
			var a, b, c uint16
			if _, err := fmt.Fscanf(reader, "%d %d %d", &a, &b, &c); err != nil {
				return nil, fmt.Errorf("bad face: %w", err)
			}
			mesh.triangles = append(mesh.triangles, triangle{
				v1: a - 1,
				v2: b - 1,
				v3: c - 1,
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

type context struct {
	view_matrix mat4
	proj_matrix mat4
	near, far   float
	viewport    viewport
}

func (c *context) set_viewport(x, y, w, h int) {
	c.viewport.x = x
	c.viewport.y = y
	c.viewport.w = w
	c.viewport.h = h
	c.viewport.w_2 = w / 2
	c.viewport.h_2 = h / 2
}

func (c *context) set_orthographic(left, right, bottom, top, near, far float32) {
	c.proj_matrix = mgl32.Ortho(left, right, bottom, top, near, far)
	c.near = near
	c.far = far
}

func (c *context) set_perpsective(fov_y, aspect, near, far float) {
	c.proj_matrix = mgl32.Perspective(fov_y, aspect, near, far)
	c.near = near
	c.far = far
}

func (c *context) look_at(eye, center, up vec3) {
	c.view_matrix = mgl32.LookAtV(eye, center, up)
}

func (c *context) projection_view_matrix() mat4 {
	return c.proj_matrix.Mul4(c.view_matrix)
}

func clip_out_of_bounds(a vec4) bool {
	x, y, z, w := a.X(), a.Y(), a.Z(), a.W()
	return x < -w || x > w || y < -w || y > w || z < -w || z > w
}

func (c *context) clip_to_ndc(src vec4) vec3 {
	w := 1.0 / src.W()
	return vec3{src.X() * w, src.Y() * w, src.Z() * w}
}

func (c *context) ndc_to_screen(src vec3) vec3 {
	w_2 := float(c.viewport.w_2)
	h_2 := float(c.viewport.h_2)
	return vec3{
		w_2*src.X() + w_2,
		h_2*src.Y() + h_2,
		0.5*src.Z() + 0.5,
	}
}

func (self *Game) Layout(outerWidth, outerHeight int) (int, int) {
	return game_width, game_height
}

func (self *Game) Update() error {
	self.cycle++
	return nil
}

func (self *Game) Draw(screen *ebiten.Image) {
	var ctx context
	w := screen.Bounds().Dx()
	h := screen.Bounds().Dy()
	ctx.set_viewport(0, 0, w, h)

	cycle := float64(self.cycle) / 200.0

	const distance = 2
	x := float(math.Cos(cycle) * distance)
	z := float(math.Sin(cycle) * distance)

	eye := vec3{x, -0.1, z}
	center := vec3{0, 0, 0}
	up := vec3{0, 1, 0}
	ctx.look_at(eye, center, up)

	// ctx.set_orthographic(-distance*game_aspect, distance*game_aspect, distance, -distance, 1, 1000)
	ctx.set_perpsective(30, game_aspect, 1, 100)

	projection_view_matrix := ctx.projection_view_matrix()

	var clip_vertices []vec4
	for _, vertex := range self.suzanne.vertices {
		vertex := projection_view_matrix.Mul4x1(vertex.position.Vec4(1))
		clip_vertices = append(clip_vertices, vertex)
	}

	type screen_triangle struct {
		first_vertex uint16
		average_z    float
		rgba         vec4
	}

	var screen_vertices []vec3
	var screen_triangles []screen_triangle

	for _, triangle := range self.suzanne.triangles {
		c0 := clip_vertices[triangle.v1]
		c1 := clip_vertices[triangle.v2]
		c2 := clip_vertices[triangle.v3]

		if clip_out_of_bounds(c0) || clip_out_of_bounds(c1) || clip_out_of_bounds(c2) {
			// TODO: clip triangle
		} else {
			ndc0 := ctx.clip_to_ndc(c0)
			ndc1 := ctx.clip_to_ndc(c1)
			ndc2 := ctx.clip_to_ndc(c2)

			// back-face culling
			if (ndc1.X()-ndc0.X())*(ndc2.Y()-ndc0.Y())-(ndc2.X()-ndc0.X())*(ndc1.Y()-ndc0.Y()) <= 0 {
				continue
			}

			s0 := ctx.ndc_to_screen(ndc0)
			s1 := ctx.ndc_to_screen(ndc1)
			s2 := ctx.ndc_to_screen(ndc2)

			screen_triangles = append(screen_triangles, screen_triangle{
				first_vertex: uint16(len(screen_vertices)),
				average_z:    (s0.Z() + s1.Z() + s2.Z()) / 3,
				rgba:         triangle.rgba,
			})

			screen_vertices = append(screen_vertices, s0, s1, s2)
		}
	}

	var vertices []ebiten.Vertex
	var indices []uint16

	slices.SortFunc(screen_triangles, func(a, b screen_triangle) int {
		if a.average_z >= b.average_z {
			return -1
		}
		return 1
	})

	// TODO: loop screen_vertices and populate vertices when we start using vertex color instead of triangle

	for _, triangle := range screen_triangles {
		rgba := triangle.rgba
		s0 := screen_vertices[triangle.first_vertex]
		s1 := screen_vertices[triangle.first_vertex+1]
		s2 := screen_vertices[triangle.first_vertex+2]

		first_index := uint16(len(indices))
		indices = append(indices, first_index, first_index+1, first_index+2)
		vertices = append(vertices,
			ebiten.Vertex{
				DstX:   s0.X(),
				DstY:   s0.Y(),
				ColorR: rgba.X(),
				ColorG: rgba.Y(),
				ColorB: rgba.Z(),
				ColorA: rgba.W(),
			},
			ebiten.Vertex{
				DstX:   s1.X(),
				DstY:   s1.Y(),
				ColorR: rgba.X(),
				ColorG: rgba.Y(),
				ColorB: rgba.Z(),
				ColorA: rgba.W(),
			},
			ebiten.Vertex{
				DstX:   s2.X(),
				DstY:   s2.Y(),
				ColorR: rgba.X(),
				ColorG: rgba.Y(),
				ColorB: rgba.Z(),
				ColorA: rgba.W(),
			},
		)
	}

	screen.DrawTriangles(vertices, indices, self.white, &ebiten.DrawTrianglesOptions{
		AntiAlias: true,
	})
}
