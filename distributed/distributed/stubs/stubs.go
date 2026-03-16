package stubs

import "uk.ac.bris.cs/gameoflife/util"

type WorkerIn struct {
	Turns       int
	Threads     int
	ImageWidth  int
	ImageHeight int
	//Height      int
	//Start       int
	World [][]byte
}

type Res struct {
	World [][]byte
	Turn  int
}

type BrokerIn struct {
	Turns       int
	Threads     int
	ImageWidth  int
	ImageHeight int
	World       [][]byte
}

type BrokerOut struct {
	World [][]byte
	Turn  int
	Cells []util.Cell
}

type CellsIn struct {
	ImageWidth  int
	ImageHeight int
	World       [][]byte
}

type CellsOut struct {
	Cells []util.Cell
	Count int
}

type AliveCellCountOut struct {
	CompletedTurns int
	AliveCellCount int
	World          [][]byte
}
