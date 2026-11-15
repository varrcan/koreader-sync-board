package uploads

import (
	"io"
	"net/http"
	"time"
)

const (
	// idleTimeout — сколько передача может простаивать. Сроки сервера (30 с) рассчитаны
	// на kosync, а 1 GiB по медленному каналу в фиксированный срок не уложить.
	idleTimeout = time.Minute
	// extendStep — не чаще этого срок сдвигается: системный вызов на каждый Read не нужен.
	extendStep = time.Second
)

// deadlineReader сдвигает срок чтения соединения вперёд по мере поступления данных,
// так что отваливается только простаивающая передача.
type deadlineReader struct {
	r          io.Reader
	rc         *http.ResponseController
	idle, step time.Duration
	extended   time.Time
}

// Body оборачивает тело запроса: срок чтения сдвигается на минуту при каждом чтении.
func Body(w http.ResponseWriter, r *http.Request) io.Reader {
	return newDeadlineReader(w, r.Body, idleTimeout, extendStep)
}

func newDeadlineReader(w http.ResponseWriter, r io.Reader, idle, step time.Duration) *deadlineReader {
	d := &deadlineReader{r: r, rc: http.NewResponseController(w), idle: idle, step: step}
	d.extend(time.Now())
	return d
}

func (d *deadlineReader) extend(now time.Time) {
	d.extended = now
	// ResponseRecorder сроков не поддерживает, ошибка не важна.
	_ = d.rc.SetReadDeadline(now.Add(d.idle))
}

func (d *deadlineReader) Read(p []byte) (int, error) {
	if now := time.Now(); now.Sub(d.extended) >= d.step {
		d.extend(now)
	}
	return d.r.Read(p)
}
