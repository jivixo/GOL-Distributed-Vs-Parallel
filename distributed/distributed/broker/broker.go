package main

import (
	"flag"
	"fmt"
	"net"
	"net/rpc"
	"strconv"
	"sync"
	"time"
	"uk.ac.bris.cs/gameoflife/stubs"
)

// distributor is publisher and workers are receivers
// to register what each worker is doing in broker and send them work to do

var broker *Broker

var shutdown bool

type Broker struct {
	Workers       []*rpc.Client
	ImgWidth      int
	ImgHeight     int
	World         [][]byte
	CompletedTurn int
	Key           sync.Mutex
	quit          bool
	Locked        bool
}

func (b *Broker) Pause(_ struct{}, response *stubs.Res) (err error) {
	for b.Locked {
		time.Sleep(10 * time.Millisecond)
	}
	b.Key.Lock()
	b.Locked = true
	fmt.Println("paused, b locked", b.Locked)
	response.World = b.World
	response.Turn = b.CompletedTurn
	return
}

func (b *Broker) Resume(_ struct{}, response *stubs.Res) (err error) {
	b.Locked = false
	b.Key.Unlock()
	fmt.Println("resume, b locked", b.Locked)
	response.World = b.World
	response.Turn = b.CompletedTurn
	return
}

func (b *Broker) Shutdown(_ struct{}, response *stubs.Res) (err error) {
	if !b.Locked {
		b.Key.Lock()
		b.Locked = true
	}
	// tell next state to stop
	b.quit = true
	shutdown = true
	response.World = b.World
	response.Turn = b.CompletedTurn
	// close all workers
	for i := 0; i < 4; i++ {
		go func() {
			err = broker.Workers[i].Call("Operations.Shutdown", struct{}{}, struct{}{})
			if err != nil {
				fmt.Println("Calling workers Shutdown wrong", err, strconv.Itoa(i))
			}
		}()
	}
	return
}

func (b *Broker) Quit(_ struct{}, response *stubs.Res) (err error) {
	if !b.Locked {
		b.Key.Lock()
		b.Locked = true
	}
	// tell next state to stop
	b.quit = true
	response.World = b.World
	response.Turn = b.CompletedTurn
	return
}

func (b *Broker) Save(_ struct{}, response *stubs.Res) (err error) {
	fmt.Println("save")
	locked := b.Locked
	if !b.Locked {
		b.Key.Lock()
		b.Locked = true
	}
	response.World = b.World
	response.Turn = b.CompletedTurn
	if !locked {
		b.Locked = false
		b.Key.Unlock()
	}
	return
}

func (b *Broker) AliveCells2Sec(_ struct{}, response *stubs.AliveCellCountOut) (err error) {
	fmt.Println("AC2sec, broker locked", b.Locked)
	if b.Locked {
		return
	}
	b.Key.Lock()
	b.Locked = true
	response.CompletedTurns = b.CompletedTurn
	aliveIn := stubs.CellsIn{
		b.ImgWidth, b.ImgHeight, b.World,
	}
	aliveOut := new(stubs.CellsOut)
	err = broker.Workers[0].Call("Operations.AliveCells", aliveIn, aliveOut)
	if err != nil {
		fmt.Println("Call ACC wrong", err)
	}
	response.AliveCellCount = aliveOut.Count
	response.World = b.World
	b.Locked = false
	b.Key.Unlock()
	return
}

// Compute connecting distributor and worker - take info from dist request and split / send to workers
func (b *Broker) Compute(request stubs.BrokerIn, response *stubs.BrokerOut) (err error) {
	// dial to all 4 workers
	broker = newBroker()
	broker.connectServer()
	fmt.Println(broker.Workers)

	// spread to diff worker to calc next state - 4 workers, split work to 4 sections
	b.ImgWidth = request.ImageWidth
	b.ImgHeight = request.ImageHeight
	height := request.ImageHeight / 4
	fmt.Println(height)
	resChan := make([]chan [][]byte, 4)
	for i := 0; i < 4; i++ {
		resChan[i] = make(chan [][]byte)
	}
	var turn int
	world := request.World
	if height == 4 {
		fmt.Println(world)
	}
	// calculate next state
	// compute every round and return
	for i := 0; i < request.Turns; i++ {
		for b.Locked {
			time.Sleep(10 * time.Millisecond)
		}
		if broker.quit {
			return
		}
		var tempWorld [][]byte
		for j := 0; j <= 3; j++ {
			partOfWorld := DivideWorld(world, height, j*height)
			if height == 4 && turn == 0 {
				fmt.Println("part", j, partOfWorld)
			}

			workerReq := stubs.WorkerIn{
				request.Turns, request.Threads, request.ImageWidth, request.ImageHeight, partOfWorld,
			}
			//let workers compute
			go func() {
				workerRes := new(stubs.Res)
				err2 := broker.Workers[j].Call("Operations.NextState", workerReq, workerRes)
				if err2 != nil {
					fmt.Println("worker", j, "incorrect", err2)
					return
				}
				resChan[j] <- workerRes.World
			}()
		}
		for j := 0; j < 4; j++ {
			part := <-resChan[j]
			tempWorld = append(tempWorld, part...)
		}
		turn++
		b.Key.Lock()
		b.Locked = true
		b.World = tempWorld
		world = tempWorld
		b.CompletedTurn = turn
		b.Locked = false
		b.Key.Unlock()
	}
	aliveIn := stubs.CellsIn{
		request.ImageWidth, request.ImageHeight, world,
	}
	aliveOut := new(stubs.CellsOut)
	server1 := broker.Workers[0]
	err = server1.Call("Operations.AliveCells", aliveIn, aliveOut)
	if err != nil {
		fmt.Println("calling AC wrong", err)
	}

	response.Cells = aliveOut.Cells
	response.World = world
	response.Turn = turn
	for i := 0; i < 4; i++ {
		close(resChan[i])
	}
	closeServers(broker)
	return
}

func main() {
	var listener net.Listener
	//listening from distributor
	pAddr := flag.String("port", "8030", "Port to listen on")
	flag.Parse()
	//rand.Seed(time.Now().UnixNano())
	err := rpc.Register(&Broker{})
	if err != nil {
		fmt.Println("Format of Operation isn't correct. ", err)
	}
	listener, err = net.Listen("tcp", ":"+*pAddr)
	if err != nil {
		fmt.Println("Broker Listening dist wrong", err)
	}
	defer listener.Close()

	// wait for client side to close
	go func() {
		for !shutdown {
			time.Sleep(500 * time.Millisecond)
		}
		fmt.Println("Broker shutting down in a few sec")
		time.Sleep(4 * time.Second)
		err = listener.Close()
		closeServers(broker)
		if err != nil {
			fmt.Println("Broker close listener wrong", err)
		}
	}()
	defer closeServers(broker)
	rpc.Accept(listener)
}

func newBroker() *Broker {
	return &Broker{
		Workers: make([]*rpc.Client, 4),
	}
}

func (b *Broker) connectServer() {
	addresses := map[int]string{ // to connect w diff server (workers) have diff address
		//0: "34.224.166.47:10000",
		//1: "54.175.61.210:10001",
		//2: "44.222.231.107:10002",
		//3: "3.227.249.19:10003","127.0.0.1:8030"
		0: "127.0.0.1:10000",
		1: "127.0.0.1:10001",
		2: "127.0.0.1:10002",
		3: "127.0.0.1:10003",
	}

	dial, err := rpc.Dial("tcp", addresses[0])
	if err != nil {
		fmt.Println("dialing 0 wrong", err)
		return
	}
	b.Workers[0] = dial

	dial, err = rpc.Dial("tcp", addresses[1])
	if err != nil {
		fmt.Println("dialing 1 wrong", err)
		return
	}
	b.Workers[1] = dial

	dial, err = rpc.Dial("tcp", addresses[2])
	if err != nil {
		fmt.Println("dialing 2 wrong", err)
		return
	}
	b.Workers[2] = dial

	dial, err = rpc.Dial("tcp", addresses[3])
	if err != nil {
		fmt.Println("dialing 3 wrong", err)
		return
	}
	b.Workers[3] = dial
}

func closeServers(b *Broker) {
	if shutdown {
		return
	}
	for i, worker := range b.Workers {
		err := worker.Close()
		if err != nil {
			fmt.Println("closing", i, "wrong", err)
		}
	}
}

func DivideWorld(world [][]byte, height int, start int) [][]byte {
	//area to compute
	var newWorld [][]byte
	// for top quarter
	if start-1 < 0 {
		newWorld = append(world[len(world)-1:], newWorld...) // last row 15
		newWorld = append(newWorld, world[0:height+1]...)    // top quarter + 1 row 01234
		return newWorld
	}
	if start+height+1 > len(world) {
		newWorld = append(newWorld, world[start-1:]...) // 1 row + bottom quarter 11 12 13 14 15
		newWorld = append(newWorld, world[0])           // top row 0
		return newWorld
	}
	// others
	newWorld = world[start-1 : start+height+1]

	return newWorld
}
