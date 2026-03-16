package gol

import (
	"flag"
	"fmt"
	"net/rpc"
	"time"
	"uk.ac.bris.cs/gameoflife/stubs"
	"uk.ac.bris.cs/gameoflife/util"
)

type distributorChannels struct {
	events     chan<- Event
	ioCommand  chan<- ioCommand
	ioIdle     <-chan bool
	ioFilename chan<- string
	ioOutput   chan<- uint8
	ioInput    <-chan uint8
	keyPresses <-chan rune
}

type Status struct {
	completedTurns int
	currentWorld   [][]byte
}

var server string

var paused bool

var quit bool

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

func createOutput(p Params, c distributorChannels, world [][]byte, turn int) {
	c.ioCommand <- ioOutput
	filename := fmt.Sprintf("%vx%vx%v", p.ImageWidth, p.ImageHeight, turn) // changed p.turns to turn
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
	status := new(Status)
	// will create a 2D slice to store world
	currentWorld := make([][]byte, p.ImageHeight)
	for i := 0; i < p.ImageWidth; i++ {
		currentWorld[i] = make([]byte, p.ImageWidth)
	}

	// this tells io to read the input image
	c.ioCommand <- ioInput
	// filename passed, is this the correct way?
	filename := fmt.Sprintf("%vx%v", p.ImageWidth, p.ImageHeight)
	c.ioFilename <- filename

	// store the output of readpgmfile into the current world
	for y := 0; y < p.ImageHeight; y++ {
		for x := 0; x < p.ImageWidth; x++ {
			currentWorld[y][x] = <-c.ioInput
		}
	}

	turn := 0

	initialAlive := aliveCells(p, currentWorld)
	for _, cell := range initialAlive {
		c.events <- CellFlipped{turn, cell}
	}
	c.events <- TurnComplete{turn}

	c.events <- StateChange{turn, Executing}
	connectServer(&server, "server", "127.0.0.1:8030", "IP:port string to connect to as server")
	flag.Parse()

	client, err1 := rpc.Dial("tcp", server)
	if err1 != nil {
		fmt.Println("dial is wrong", err1)
	}
	defer client.Close()

	// 2 second ticker go routine and a RPC call that is made on every tick to ask the worker for AliveCellCount
	ticker := time.NewTicker(2 * time.Second)
	stop := make(chan struct{})

	go func() {
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if paused {
					continue
				}
				if quit {
					return
				}
				out := new(stubs.AliveCellCountOut)
				err := client.Call("Broker.AliveCells2Sec", struct{}{}, out)
				if err != nil {
					fmt.Println("Broker.AliveCells2Sec RPC failed", err)
					continue
				}
				// Here AliveCellsCount is not the same as the worker function AliveCellCount
				// send AliveCellsCount event for the tests: uses data from worker’s AliveCount RPC
				c.events <- AliveCellsCount{
					CompletedTurns: out.CompletedTurns,
					CellsCount:     out.AliveCellCount,
				}
				status.completedTurns = out.CompletedTurns
				status.currentWorld = out.World
			}
		}
	}()

	paused = false
	quit = false

	go func() {
		for {
			for paused && !quit { // under pause
				select {
				case key := <-c.keyPresses:
					switch key {
					case 's': //generate a PGM file with the current state of the board.
						resp := new(stubs.Res)
						err := client.Call("Broker.Save", struct{}{}, resp)
						if err != nil {
							fmt.Println("p+s", err)
						}
						createOutput(p, c, resp.World, resp.Turn)
					case 'q': //close the controller client program without causing an error on the Gol server.
						resp := new(stubs.Res)
						err := client.Call("Broker.Quit", struct{}{}, resp)
						if err != nil {
							fmt.Println("paused and quit", err)
						}
						createOutput(p, c, resp.World, resp.Turn)
						quit = true
						paused = false
						c.events <- StateChange{resp.Turn, Quitting}
						return
					case 'p': //resume the processing and have the controller print Continuing.
						resp := new(stubs.Res)
						err := client.Call("Broker.Resume", struct{}{}, resp)
						if err != nil {
							fmt.Println("resume", err)
						}
						paused = false
						// state is changed from paused to executing
						c.events <- StateChange{resp.Turn, Executing}
					case 'k':
						resp := new(stubs.Res)
						err := client.Call("Broker.Shutdown", struct{}{}, resp)
						if err != nil {
							fmt.Println("p + shutdown", err)
						}
						createOutput(p, c, resp.World, resp.Turn)
						quit = true
						fmt.Println("shutting down in a few sec")
						c.events <- StateChange{status.completedTurns, Quitting}
						return
					}
				}
			}

			select { // under active
			case key := <-c.keyPresses:
				switch key {
				case 's': //generate a PGM file with the current state of the board.
					resp := new(stubs.Res)
					err := client.Call("Broker.Save", struct{}{}, resp)
					if err != nil {
						fmt.Println("save", err)
					}
					createOutput(p, c, resp.World, resp.Turn)
				case 'p': //pause the processing on the AWS node and have the controller print the current turn that is being processed.
					paused = true
					resp := new(stubs.Res)
					err := client.Call("Broker.Pause", struct{}{}, resp)
					if err != nil {
						fmt.Println("paused", err)
					}
					// state is changed from executing to paused
					c.events <- StateChange{resp.Turn, Paused}
				case 'q': //close the controller client program without causing an error on the Gol server.
					resp := new(stubs.Res)
					err := client.Call("Broker.Quit", struct{}{}, resp)
					if err != nil {
						fmt.Println("quit", err)
					}
					createOutput(p, c, resp.World, resp.Turn)
					quit = true
					c.events <- StateChange{resp.Turn, Quitting}
					return
				case 'k': // stop both client and server and print pgm
					resp := new(stubs.Res)
					err := client.Call("Broker.Shutdown", struct{}{}, resp)
					if err != nil {
						fmt.Println("shutdown", err)
					}
					createOutput(p, c, resp.World, resp.Turn)
					quit = true
					fmt.Println("shutting down in a few sec")
					c.events <- StateChange{resp.Turn, Quitting}
					return
				}
			default:
			}
		}
	}()

	n := 0
	//aliveOut := new(stubs.CellsOut)
	for !quit && n < 1 {
		request := stubs.BrokerIn{
			p.Turns, p.Threads, p.ImageWidth, p.ImageHeight, currentWorld,
		}
		response := new(stubs.BrokerOut)
		err := client.Call("Broker.Compute", request, response)
		if err != nil {
			fmt.Println("call Compute is wrong", err)
		} // need something to tell next state stop working

		turn = response.Turn
		currentWorld = response.World
		cells := response.Cells

		// Make sure that the Io has finished any output before exiting.
		c.ioCommand <- ioCheckIdle
		<-c.ioIdle
		createOutput(p, c, currentWorld, turn)

		// final state is reported here using the FinalTurnCompleteEvent
		c.events <- FinalTurnComplete{turn, cells}
		n++
	}
	// ticker is shutdown after NextState RPC Finishes
	close(stop)
	ticker.Stop()
	// Close the channel to stop the SDL goroutine gracefully. Removing may cause deadlock.
	close(c.events)
}

// prevent flag redefine - needed?
func connectServer(p *string, name string, value string, usage string) {
	if flag.Lookup(name) == nil {
		flag.StringVar(p, name, value, usage)
	}
}
