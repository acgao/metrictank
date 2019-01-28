package kafkamdm

import (
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

type OffsetAdjuster struct {
	readOffset, highWaterMark int64
	ts                        time.Time
	lag                       *lagLogger
}

func (o *OffsetAdjuster) add(msgsProcessed, msgsAdded int64, secondsPassed int) {
	o.ts = o.ts.Add(time.Second * time.Duration(secondsPassed))
	o.readOffset += msgsProcessed
	o.highWaterMark += msgsAdded
	o.lag.Store(o.readOffset, o.highWaterMark, o.ts)
}

func TestLagLogger(t *testing.T) {
	logger := newLagLogger(5)

	Convey("with 0 measurements", t, func() {
		So(logger.Min(), ShouldEqual, -1)
	})

	adjuster := OffsetAdjuster{
		readOffset:    10 * 1000 * 1000,
		highWaterMark: 10 * 1000 * 1000,
		ts:            time.Now(),
		lag:           logger,
	}
	Convey("with 1 measurements", t, func() {
		adjuster.add(0, 10, 1)
		So(logger.Min(), ShouldEqual, 10)
		So(logger.Rate(), ShouldEqual, 0)
	})
	Convey("with 2 measurements", t, func() {
		adjuster.add(10, 5, 1)
		So(logger.Min(), ShouldEqual, 5)
		So(logger.Rate(), ShouldEqual, 5)
	})
	Convey("with a negative measurement", t, func() {
		// Negative measurements are discarded, should be same as last time.
		// Add directly to not mess with the adjusters offsets
		logger.Store(adjuster.readOffset, adjuster.highWaterMark-100, adjuster.ts)
		So(logger.Min(), ShouldEqual, 5)
		So(logger.Rate(), ShouldEqual, 5)
	})
	Convey("with lots of measurements", t, func() {
		// fall behind by 1 each step (we started behind by 5)
		for i := 0; i < 100; i++ {
			adjuster.add(19, 20, 1)
		}
		So(logger.Min(), ShouldEqual, 101)
		So(logger.Rate(), ShouldEqual, 20)
	})
}

func TestLagWithShortProcessingPause(t *testing.T) {
	logger := newLagLogger(5)

	adjuster := OffsetAdjuster{
		readOffset:    10 * 1000 * 1000,
		highWaterMark: 10 * 1000 * 1000,
		ts:            time.Now(),
		lag:           logger,
	}

	// start with 50 lag
	adjuster.highWaterMark += 50

	// Simulate being almost in sync
	for i := 0; i < 100; i++ {
		adjuster.add(5000, 5000, 5)
	}

	Convey("should be almost in sync", t, func() {
		So(logger.Min(), ShouldEqual, 50)
		So(logger.Rate(), ShouldEqual, 1000)
	})

	// Short pause with no msgs processed
	adjuster.add(0, 5*1000, 5)

	Convey("Short pause should not cause large lag estimate", t, func() {
		So(logger.Min(), ShouldEqual, 50)
		So(logger.Rate(), ShouldEqual, 1000)
	})
}

// TestLagMonitor tests LagMonitor priorities based on various scenarios.
// the overall priority is obviously simply the max priority of any of the partitions,
// so for simplicity we can focus on simulating 1 partition.
func TestLagMonitor(t *testing.T) {
	start := time.Now()
	mon := NewLagMonitor(2, []int32{0})

	// advance records a state change (newest and current offsets) for the given timestamp
	advance := func(sec int, newest, offset int64) {
		ts := start.Add(time.Second * time.Duration(sec))
		mon.StoreOffsets(0, offset, newest, ts)
	}

	Convey("with 0 measurements, priority should be 10k", t, func() {
		So(mon.Metric(), ShouldEqual, 10000)
		Reset(func() { mon = NewLagMonitor(2, []int32{0}) })
		Convey("with 100 measurements, not consuming and lag just growing", func() {
			for i := 0; i < 100; i++ {
				advance(i, int64(i), 0)
			}
			So(mon.Metric(), ShouldEqual, 98) // min-lag(98) / rate (0) = 98
		})
		Convey("with 100 measurements, each advancing 1 offset per second, and lag growing by 1 each second (e.g. real rate is 2/s)", func() {
			for i := 0; i < 100; i++ {
				advance(i, int64(i*2), int64(i))
			}
			So(mon.Metric(), ShouldEqual, 49) // min-lag(98) / input rate (2) = 49
		})
		Convey("rate of production is 100k and lag is 1000", func() {
			advance(1, 100000, 99000)
			advance(2, 200000, 199000)
			So(mon.Metric(), ShouldEqual, 0) // 1000 / 100k = 0
			Convey("rate of production goes up to 200k but lag stays consistent at 1000", func() {
				advance(3, 400000, 399000)
				advance(4, 600000, 599000)
				So(mon.Metric(), ShouldEqual, 0) // 1000 / 200k = 0
			})
			Convey("rate of production goes down to 1000 but lag stays consistent at 1000", func() {
				advance(3, 201000, 200000)
				advance(4, 202000, 201000)
				So(mon.Metric(), ShouldEqual, 1) // 1000 / 1000 = 1
			})
			Convey("rate of production goes up to 200k but we can only keep up with the rate of 100k so lag starts growing", func() {
				advance(3, 400000, 299000)
				advance(4, 600000, 399000)
				So(mon.Metric(), ShouldEqual, 0) // (400000-299000)/200000 = 0
				advance(5, 800000, 499000)       // note: we're now where the producer was at +- t=3.5, so 1.5s behind
				So(mon.Metric(), ShouldEqual, 1) // (600000-399000)/200000 = 1
				advance(6, 1000000, 599000)      // note: we're now at where the producer was at +- t=4, so 2 seconds behind
				So(mon.Metric(), ShouldEqual, 1) // (800000-499000)/200000 = 1
				advance(15, 2800000, 1499000)
				advance(16, 3000000, 1599000)    // Jump forward 10 seconds, where the producer was at +- t=9, so 7 seconds behind
				So(mon.Metric(), ShouldEqual, 6) // (2800000-1499000)/200000 = 6
			})
			Convey("a GC pause is causing us to not be able to consume during a few seconds", func() {
				advance(3, 300000, 199000)
				advance(4, 400000, 199000)
				So(mon.Metric(), ShouldEqual, 1) // ~(300000-199000)/100000 = 1
				// test what happens during recovery
				advance(5, 500000, 499000)
				So(mon.Metric(), ShouldEqual, 0) // ~(500000-499000)/100000 = 0
			})
		})
	})
}
