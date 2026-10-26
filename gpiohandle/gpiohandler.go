package gpiohandler

import (
    "fmt"
    "time"

    "github.com/warthog618/go-gpiocdev"
)

type Config struct {
    InputPins     []int
    OutputPins    []int
    OnInputChange func(pin int, state int)
}

type GPIOHandler struct {
    cfg     Config
    chip    *gpiocdev.Chip
    inLines map[int]*gpiocdev.Line
    outLines map[int]*gpiocdev.Line
}

// New sets up the GPIO lines
func New(cfg Config) (*GPIOHandler, error) {
    chip, err := gpiocdev.NewChip("gpiochip0")
    if err != nil {
        return nil, fmt.Errorf("open chip: %v", err)
    }

    h := &GPIOHandler{
        cfg:      cfg,
        chip:     chip,
        inLines:  make(map[int]*gpiocdev.Line),
        outLines: make(map[int]*gpiocdev.Line),
    }

    for _, pin := range cfg.InputPins {
        
// line, err := chip.RequestLine(pin, gpiocdev.AsInput, gpiocdev.WithPullDown)
line, err := chip.RequestLine(pin, gpiocdev.AsInput)       
 if err != nil {
            fmt.Printf("[GPIO] Failed to request input %d: %v\n", pin, err)
            continue
        }
        h.inLines[pin] = line
        fmt.Printf("[GPIO] Input pin %d ready\n", pin)
    }

    for _, pin := range cfg.OutputPins {
        line, err := chip.RequestLine(pin, gpiocdev.AsOutput(0))
        if err != nil {
            fmt.Printf("[GPIO] Failed to request output %d: %v\n", pin, err)
            continue
        }
        h.outLines[pin] = line
        fmt.Printf("[GPIO] Output pin %d ready\n", pin)
    }

    return h, nil
}

// Start just polls all input pins continuously.
func (h *GPIOHandler) Start() {
    for pin, line := range h.inLines {
        go func(pin int, line *gpiocdev.Line) {
            last := -1
            for {
                v, err := line.Value()
                if err != nil {
                    time.Sleep(50 * time.Millisecond)
                    continue
                }
                if v != last {
                    last = v
                    fmt.Printf("[GPIO] Pin %d changed to %d\n", pin, v)
                    if h.cfg.OnInputChange != nil {
                        h.cfg.OnInputChange(pin, v)
                    }
                }
                time.Sleep(30 * time.Millisecond)
            }
        }(pin, line)
    }
}

// SetOutput sets an output line high or low.
func (h *GPIOHandler) SetOutput(pin int, state bool) {
    line, ok := h.outLines[pin]
    if !ok {
        fmt.Printf("[GPIO] Invalid output %d\n", pin)
        return
    }
    v := 0
    if state {
        v = 1
    }
    if err := line.SetValue(v); err != nil {
        fmt.Printf("[GPIO] Set output %d failed: %v\n", pin, err)
    }
}