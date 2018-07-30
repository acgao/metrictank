package expr

import (
	"fmt"
	"math"
	"strconv"

	"github.com/grafana/metrictank/api/models"
	schema "gopkg.in/raintank/schema.v1"
)

type FuncScaleToSeconds struct {
	in      GraphiteFunc
	seconds float64
}

func NewScaleToSeconds() GraphiteFunc {
	return &FuncScaleToSeconds{}
}

func (s *FuncScaleToSeconds) Signature() ([]Arg, []Arg) {
	return []Arg{
		ArgSeriesList{val: &s.in},
		ArgFloat{key: "seconds", val: &s.seconds},
	}, []Arg{ArgSeriesList{}}
}

func (s *FuncScaleToSeconds) Context(context Context) Context {
	return context
}

func (s *FuncScaleToSeconds) Exec(cache map[Req][]models.Series) ([]models.Series, error) {
	series, err := s.in.Exec(cache)
	if err != nil {
		return nil, err
	}

	out := make([]models.Series, len(series))
	for i, serie := range series {
		transformed := &out[i]
		transformed.Target = fmt.Sprintf("scaleToSeconds(%s,%d)", serie.Target, int64(s.seconds))
		transformed.QueryPatt = transformed.Target
		transformed.Tags = make(map[string]string, len(serie.Tags)+1)
		transformed.Datapoints = pointSlicePool.Get().([]schema.Point)
		transformed.Interval = serie.Interval
		transformed.Consolidator = serie.Consolidator
		transformed.QueryCons = serie.QueryCons

		for k, v := range serie.Tags {
			transformed.Tags[k] = v
		}
		transformed.Tags["scaleToSeconds"] = strconv.FormatFloat(s.seconds, 'g', -1, 64)

		factor := float64(s.seconds) / float64(serie.Interval)
		for _, p := range serie.Datapoints {
			if !math.IsNaN(p.Val) {
				// round to 6 decimal places to mimic graphite
				p.Val = round(p.Val*factor, 6)
			}
			transformed.Datapoints = append(transformed.Datapoints, p)
		}
	}
	cache[Req{}] = append(cache[Req{}], out...)
	return out, nil
}

func round(val float64, places int) (newVal float64) {
	var round float64
	pow := math.Pow(10, float64(places))
	digit := pow * val
	_, div := math.Modf(digit)
	if div >= 0.5 {
		round = math.Ceil(digit)
	} else {
		round = math.Floor(digit)
	}
	newVal = round / pow
	return
}
