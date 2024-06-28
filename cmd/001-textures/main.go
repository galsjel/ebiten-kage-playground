package main

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
	"math"
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

var shader_source = `
//kage:unit pixels
package main

func Fragment(dst vec4, src vec2, color vec4) vec4 {
	return imageSrc0At(src)
}
`

//go:embed wall.obj
var wall_obj []byte

//go:embed diffuse.jpg
var diffuse_jpg []byte
var diffuse *ebiten.Image

func main() {
	shader, err := ebiten.NewShader([]byte(shader_source))

	if err != nil {
		panic(err)
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
		shader: shader,
		image:  ebiten.NewImageFromImage(image),
		mesh:   mesh,
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
	shader    *ebiten.Shader
	image     *ebiten.Image
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
	v1, v2, v3 uint16
	t1, t2, t3 uint16
}

type mesh struct {
	vertices  []vec3
	triangles []triangle
	texcoords []vec2
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
			mesh.vertices = append(mesh.vertices, vec3{x, y, z})
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
				v1: v1 - 1,
				v2: v2 - 1,
				v3: v3 - 1,
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

	ctx.set_perpsective(30, game_aspect, 0.1, 100)

	// the camera view matrix is invalid until the user controls it
	if self.camera.view_matrix.Det() == 0 {
		ctx.look_at(
			vec3{0, 10, -10},
			vec3{0, 10, 10},
			vec3{0, 1, 0},
		)
	} else {
		ctx.view_matrix = self.camera.view_matrix
	}

	projection_view_matrix := ctx.projection_view_matrix()

	var clip_vertices []vec4
	for _, vertex := range self.mesh.vertices {
		vertex := projection_view_matrix.Mul4x1(vertex.Vec4(1))
		clip_vertices = append(clip_vertices, vertex)
	}

	type screen_triangle struct {
		index        uint16
		first_vertex uint16
		average_z    float
	}

	var screen_vertices []vec3
	var screen_triangles []screen_triangle

	for index, triangle := range self.mesh.triangles {
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
				index:        uint16(index),
				first_vertex: uint16(len(screen_vertices)),
				average_z:    (s0.Z() + s1.Z() + s2.Z()) / 3,
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
		s1 := screen_vertices[triangle.first_vertex]
		s2 := screen_vertices[triangle.first_vertex+1]
		s3 := screen_vertices[triangle.first_vertex+2]

		t := self.mesh.triangles[triangle.index]
		t1 := self.mesh.texcoords[t.t1]
		t2 := self.mesh.texcoords[t.t2]
		t3 := self.mesh.texcoords[t.t3]

		tex_width := float(self.image.Bounds().Dx())
		tex_height := float(self.image.Bounds().Dy())

		first_index := uint16(len(indices))
		indices = append(indices, first_index, first_index+1, first_index+2)
		vertices = append(vertices,
			ebiten.Vertex{
				SrcX:   t1[0] * tex_width,
				SrcY:   t1[1] * tex_height,
				DstX:   s1.X(),
				DstY:   s1.Y(),
				ColorR: 1,
				ColorG: 1,
				ColorB: 1,
				ColorA: 1,
			},
			ebiten.Vertex{
				SrcX:   t2[0] * tex_width,
				SrcY:   t2[1] * tex_height,
				DstX:   s2.X(),
				DstY:   s2.Y(),
				ColorR: 1,
				ColorG: 1,
				ColorB: 1,
				ColorA: 1,
			},
			ebiten.Vertex{
				SrcX:   t3[0] * tex_width,
				SrcY:   t3[1] * tex_height,
				DstX:   s3.X(),
				DstY:   s3.Y(),
				ColorR: 1,
				ColorG: 1,
				ColorB: 1,
				ColorA: 1,
			},
		)
	}

	screen.DrawTriangles(vertices, indices, self.image, &ebiten.DrawTrianglesOptions{
		AntiAlias: true,
	})

	ebitenutil.DebugPrint(screen, fmt.Sprintf("FPS: %.0f (%v)", ebiten.ActualTPS(), self.frametime))
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Triangles: %d", len(screen_triangles)), 0, 14)
}
