package main

import (
	"flag"
	"fmt"
	"net"
	"net/rpc"
	"sync"
	"time"

	"uk.ac.bris.cs/gameoflife/stubs"
	"uk.ac.bris.cs/gameoflife/util"
)

type Params struct {
	Turns       int
	Threads     int
	ImageWidth  int
	ImageHeight int
}

// to store Live state (current world and completed turns) for Distributed step 2
type Operations struct {
	quit bool
}

var listener net.Listener

var shutdown bool

func (s *Operations) Shutdown(_ struct{}, _ *stubs.Res) (err error) {
	// tell next state to stop
	s.quit = true
	shutdown = true
	//close(shutdown)
	return
}

// Alive cells reads the cells that are alive right now in the worker and returns the alive cells
func (s *Operations) AliveCells(request stubs.CellsIn, response *stubs.CellsOut) (err error) {
	var result []util.Cell
	var count int
	for y := 0; y < request.ImageHeight; y++ {
		for x := 0; x < request.ImageWidth; x++ {
			if request.World[y][x] == 255 {
				result = append(result, util.Cell{x, y})
				count++
			}
		}
	}
	response.Cells = result
	response.Count = count
	return
}

func (s *Operations) NextState(request stubs.WorkerIn, response *stubs.Res) (err error) {
	currentWorld := request.World
	p := Params{
		request.Turns,
		request.Threads,
		request.ImageWidth,
		request.ImageHeight,
	}
	lenOfWorld := len(currentWorld)

	workerCount := p.Threads
	if s.quit {
		return
	}
	currentWorld = nextStateParallel(p, currentWorld, workerCount, lenOfWorld) // len = 6
	response.World = currentWorld[1 : lenOfWorld-1]
	return
}

func countAliveNeighbours(world [][]byte, x, y, width, height int) int {
	count := 0
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}
			nx := (x + dx + width) % width
			ny := y + dy
			if world[ny][nx] == 255 {
				count++
			}
		}
	}
	return count
}

type rowResult struct {
	y    int    // the row index it processed
	data []byte // the computed byte slice for that row
}

// Each worker processes a single row at a time
func worker(p Params, world [][]byte, jobs <-chan int, results chan<- rowResult, wg *sync.WaitGroup, height int) {
	// when the job is closed, it will exit and call wg.Done()
	defer wg.Done()
	for y := range jobs {
		if y > height { // height = 6
			return
		}
		row := make([]byte, p.ImageWidth)
		for x := 0; x < p.ImageWidth; x++ {
			neighbours := countAliveNeighbours(world, x, y, p.ImageWidth, height) // y = 1234 , x = whole row
			cell := world[y][x]

			if cell == 255 && (neighbours < 2 || neighbours > 3) {
				row[x] = 0
			} else if cell == 0 && neighbours == 3 {
				row[x] = 255
			} else {
				row[x] = cell
			}
		}
		results <- rowResult{y: y, data: row}
	}
}

// Runs one full generation using a worker pool
func nextStateParallel(p Params, world [][]byte, workerCount int, lenOfWorld int) [][]byte {
	var wg sync.WaitGroup
	height := lenOfWorld // = 6
	width := p.ImageWidth

	newWorld := make([][]byte, height)
	for i := range newWorld {
		newWorld[i] = make([]byte, width)
	}

	jobs := make(chan int, height-2)          // = 4
	results := make(chan rowResult, height-2) // = 4

	// Launch workers
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go worker(p, world, jobs, results, &wg, height)
	}

	// Send all row jobs
	for y := 1; y < lenOfWorld-1; y++ { // 012345 - 1234
		jobs <- y
	}
	close(jobs)

	// Collect results asynchronously
	go func() {
		wg.Wait()
		close(results) // need to close to close loop
	}()

	// for every rowResult value r it recieves, it writes the row into the correct place in new world
	// this is done using r.y, which is the row index
	for r := range results {
		newWorld[r.y] = r.data
	}

	return newWorld
}

// server
func main() {
	pAddr := flag.String("port", "10003", "Port to listen on")
	flag.Parse()
	err := rpc.Register(&Operations{})
	if err != nil {
		fmt.Println("Format of Operation isn't correct. ", err)
	}
	listener, _ = net.Listen("tcp", ":"+*pAddr)
	defer listener.Close()
	go func() {
		for !shutdown {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(2 * time.Second)
		listener.Close()
	}()

	rpc.Accept(listener)
}
