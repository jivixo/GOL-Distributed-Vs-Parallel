package gol

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"uk.ac.bris.cs/gameoflife/util"
)

type distributorChannels struct {
	events     chan<- Event
	ioCommand  chan<- ioCommand
	ioIdle     <-chan bool
	ioFilename chan<- string
	ioOutput   chan<- byte
	ioInput    <-chan byte
	keyPresses <-chan rune
}

type tickerState struct {
	mu    sync.Mutex
	cond  *sync.Cond
	alive bool // becomes true every 2 seconds
	stop  bool // set to true when distributor finishes
}

type sdlState struct {
	mu    sync.Mutex
	cond  *sync.Cond
	queue []Event // used to enqueue events later on
	stop  bool    // needed to stop the goroutine and exit gracefully
}

// function to return alive cells
func aliveCells(p Params, world [][]byte) []util.Cell {
	var result []util.Cell
	for y := 0; y < p.ImageHeight; y++ {
		for x := 0; x < p.ImageWidth; x++ {
			if world[y][x] == 255 {
				result = append(result, util.Cell{x, y})
			}
		}
	}
	return result
}

func countAliveNeighbours(world [][]byte, x, y, width, height int) int {
	w := width - 1
	h := height - 1

	rowAbove := world[(y-1)&h]
	row := world[y]
	rowBelow := world[(y+1)&h]

	count := 0

	// Unrolled 8-neighbour check
	if rowAbove[(x-1)&w] == 255 {
		count++
	}
	if rowAbove[x] == 255 {
		count++
	}
	if rowAbove[(x+1)&w] == 255 {
		count++
	}

	if row[(x-1)&w] == 255 {
		count++
	}
	if row[(x+1)&w] == 255 {
		count++
	}

	if rowBelow[(x-1)&w] == 255 {
		count++
	}
	if rowBelow[x] == 255 {
		count++
	}
	if rowBelow[(x+1)&w] == 255 {
		count++
	}
	return count
}

// Each worker processes a single row at a time
// computes the next state of that row, records any cells that changed, and sends the result back
func worker(p Params, currentWorld, nextWorld [][]byte, nextRow *int32, flipsByRow [][]util.Cell, wg *sync.WaitGroup) {
	// when the job is closed, it will exit and call wg.Done()
	defer wg.Done()

	for {
		// Fetch next row to work on (atomic)
		y := int(atomic.AddInt32(nextRow, 1) - 1)
		if y >= p.ImageHeight {
			return // no more rows
		}

		// compute row y into nextWorld[y] directly
		// flipsByRow[y] is unique to this row
		var flips []util.Cell
		rowOut := nextWorld[y]
		rowIn := currentWorld[y]

		for x := 0; x < p.ImageWidth; x++ {
			neighbours := countAliveNeighbours(currentWorld, x, y, p.ImageWidth, p.ImageHeight)
			cell := rowIn[x]
			var newCell byte

			if cell == 255 && (neighbours < 2 || neighbours > 3) {
				newCell = 0
			} else if cell == 0 && neighbours == 3 {
				newCell = 255
			} else {
				newCell = cell
			}

			rowOut[x] = newCell
			if newCell != cell {
				flips = append(flips, util.Cell{x, y})
			}
		}

		// store flips into the flipsByRow slot
		flipsByRow[y] = flips
	}
}

// Runs one full generation using a worker pool
func nextStateParallel(p Params, currentWorld, nextWorld [][]byte, workerCount int) []util.Cell {
	// Preallocate flipsByRow: each row gets its own slice to avoid contention
	flipsByRow := make([][]util.Cell, p.ImageHeight)

	var nextRow int32 = 0 // atomic counter starting at 0, workers do Add(1) to get row index

	var wg sync.WaitGroup

	// Launch workers
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go worker(p, currentWorld, nextWorld, &nextRow, flipsByRow, &wg)
	}

	// Wait for all workers to finish
	wg.Wait()

	var flips []util.Cell

	// Collect flips from all rows
	for y := 0; y < p.ImageHeight; y++ {
		if len(flipsByRow[y]) > 0 {
			flips = append(flips, flipsByRow[y]...)
		}
	}
	return flips
}

// ticker goroutine
func aliveCount2Sec(ts *tickerState) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop() // prevents goroutines from leaking

	for {
		select {
		case <-ticker.C:
			ts.mu.Lock()
			if ts.stop {
				ts.mu.Unlock()
				return
			}
			ts.alive = true // signal distributor that a tick occurred
			ts.cond.Broadcast()
			ts.mu.Unlock()
		}
	}
}

// sdl goroutine
func sdlRoutine(s *sdlState, c distributorChannels) {
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.stop {
			s.cond.Wait()
		}
		if s.stop && len(s.queue) == 0 {
			s.mu.Unlock()
			return
		}
		// read the first out of the queue
		evt := s.queue[0]
		// re-slice to remove the first out of the queue
		s.queue = s.queue[1:]
		s.mu.Unlock()

		c.events <- evt
	}
}

func createOutput(p Params, c distributorChannels, world [][]byte, turn int) {
	c.ioCommand <- ioOutput
	// changed p.Turns to turn because the filename of the output was wrong
	filename := fmt.Sprintf("%vx%vx%v", p.ImageWidth, p.ImageHeight, turn)
	c.ioFilename <- filename

	for y := 0; y < p.ImageHeight; y++ {
		for x := 0; x < p.ImageWidth; x++ {
			c.ioOutput <- world[y][x]
		}
	}

	c.ioCommand <- ioCheckIdle
	<-c.ioIdle
	// sends an event through the events channel to notify that the PGM is saved
	c.events <- ImageOutputComplete{turn, filename}
}

// distributor divides the work between workers and interacts with other goroutines.
func distributor(p Params, c distributorChannels) {
	// will create a 2D slice to store world
	currentWorld := make([][]byte, p.ImageHeight)
	nextWorld := make([][]byte, p.ImageHeight)
	for i := 0; i < p.ImageHeight; i++ {
		currentWorld[i] = make([]byte, p.ImageWidth)
		nextWorld[i] = make([]byte, p.ImageWidth)
	}

	// this tells io to read the input image
	c.ioCommand <- ioInput
	filename := fmt.Sprintf("%vx%v", p.ImageWidth, p.ImageHeight)
	c.ioFilename <- filename

	// store the output of readpgmfile into the current world
	for y := 0; y < p.ImageHeight; y++ {
		for x := 0; x < p.ImageWidth; x++ {
			currentWorld[y][x] = <-c.ioInput
		}
	}

	turn := 0

	// send initial alive cells as a cellsflipped event before the first state change
	initialAlive := aliveCells(p, currentWorld)
	if len(initialAlive) > 0 {
		c.events <- CellsFlipped{turn, initialAlive}
	}

	c.events <- StateChange{turn, Executing}

	ts := tickerState{}            // create a new instance of the struct
	ts.cond = sync.NewCond(&ts.mu) // create a conditonal variable where the wait and signal is protected by ts.mu
	go aliveCount2Sec(&ts)

	sdl := &sdlState{}
	sdl.cond = sync.NewCond(&sdl.mu)
	go sdlRoutine(sdl, c)

	workerCount := p.Threads
	paused := false
	quit := false

	for i := 0; i < p.Turns; i++ {
		for paused && !quit {
			select {
			case key := <-c.keyPresses:
				switch key {
				case 's':
					createOutput(p, c, currentWorld, turn)
				case 'q':
					quit = true
					paused = false
				case 'p':
					paused = false
					// state is changed from paused to executing
					c.events <- StateChange{turn, Executing}
				}
			default:
			}
		}

		// during active simulation
		select {
		case key := <-c.keyPresses:
			switch key {
			case 's':
				createOutput(p, c, currentWorld, turn)
			case 'p':
				paused = true
				// state is changed from executing to paused
				c.events <- StateChange{turn, Paused}
			case 'q':
				quit = true
			}
		default:
		}

		// calculated the next generation of the GOL grid
		flips := nextStateParallel(p, currentWorld, nextWorld, workerCount)
		currentWorld, nextWorld = nextWorld, currentWorld

		turn++

		// Check ticker signal using cond and mutex
		ts.mu.Lock()
		if ts.alive {
			ts.alive = false
			ts.mu.Unlock()
			c.events <- AliveCellsCount{turn, len(aliveCells(p, currentWorld))}
		} else {
			ts.mu.Unlock()
		}

		if len(flips) > 0 {
			sdl.mu.Lock()
			sdl.queue = append(sdl.queue, CellsFlipped{turn, flips})
			sdl.cond.Broadcast()
			sdl.mu.Unlock()
		}

		// enqueue TurnComplete after CellsFlipped
		sdl.mu.Lock()
		sdl.queue = append(sdl.queue, TurnComplete{turn})
		sdl.cond.Broadcast()
		sdl.mu.Unlock()

		// if quit then exit and run code after the loop
		if quit {
			break
		}
	}

	// final state is reported here using the FinalTurnCompleteEvent
	c.events <- FinalTurnComplete{turn, aliveCells(p, currentWorld)}
	createOutput(p, c, currentWorld, turn)

	// exit goroutine gracefully
	ts.mu.Lock()
	ts.stop = true
	ts.cond.Broadcast()
	ts.mu.Unlock()

	// exit goroutine gracefully
	sdl.mu.Lock()
	sdl.stop = true
	sdl.cond.Broadcast()
	sdl.mu.Unlock()

	// statechange after 'q' is passed
	c.events <- StateChange{turn, Quitting}

	// Make sure that the Io has finished any output before exiting.
	c.ioCommand <- ioCheckIdle
	<-c.ioIdle

	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(c.events)
}
