package probe

/*
static int answer(int value) { return value * 6; }
*/
import "C"

func Answer(value int) int { return int(C.answer(C.int(value))) }
