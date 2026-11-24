package abs

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
)

// Segment is one Yoto track cut from a single audio file.
type Segment struct {
	Title string
	Ino   string  // audio file
	Ext   string  // file extension without dot, e.g. "mp3", "m4b"
	Start float64 // seconds within the file
	End   float64 // seconds within the file
	Whole bool    // covers the entire file (can be streamed as-is when mp3)
}

func (s Segment) Duration() float64 { return s.End - s.Start }

// PartLength is the length of parts when a long file has no chapters.
const PartLength = 30 * 60

// minSegment drops slivers left by rounding at file boundaries.
const minSegment = 1.0

type fileSpan struct {
	AudioFile
	offset float64 // start of this file on the book's timeline
}

// Plan splits a book into Yoto tracks:
//   - chapters, when the book has them (cut at file boundaries if a chapter
//     spans files, named "Title (1/2)" etc.);
//   - otherwise one track per audio file, with any file longer than an hour
//     split into 30-minute parts.
func Plan(item *Item) ([]Segment, error) {
	files := includedFiles(item.Media.AudioFiles)
	if len(files) == 0 {
		return nil, fmt.Errorf("this item has no audio files")
	}
	var spans []fileSpan
	var total float64
	for _, f := range files {
		spans = append(spans, fileSpan{AudioFile: f, offset: total})
		total += f.Duration
	}

	if chapters := usableChapters(item.Media.Chapters, total); len(chapters) > 0 {
		var segs []Segment
		for _, ch := range chapters {
			pieces := cutAcrossFiles(spans, ch.Start, math.Min(ch.End, total))
			for i, p := range pieces {
				p.Title = ch.Title
				if len(pieces) > 1 {
					p.Title = fmt.Sprintf("%s (%d/%d)", ch.Title, i+1, len(pieces))
				}
				segs = append(segs, p)
			}
		}
		return segs, nil
	}

	title := item.Media.Metadata.Title
	var segs []Segment
	for i, f := range spans {
		name := strings.TrimSuffix(f.Metadata.Filename, filepath.Ext(f.Metadata.Filename))
		if len(spans) == 1 || name == "" {
			name = title
			if len(spans) > 1 {
				name = fmt.Sprintf("%s – Part %d", title, i+1)
			}
		}
		if f.Duration <= 2*PartLength {
			segs = append(segs, Segment{Title: name, Ino: f.Ino, Ext: ext(f.AudioFile), Start: 0, End: f.Duration, Whole: true})
			continue
		}
		n := int(math.Ceil(f.Duration / PartLength))
		for p := 0; p < n; p++ {
			start := float64(p) * PartLength
			end := math.Min(start+PartLength, f.Duration)
			if end-start < minSegment {
				continue
			}
			segs = append(segs, Segment{
				Title: fmt.Sprintf("%s (%d/%d)", name, p+1, n),
				Ino:   f.Ino, Ext: ext(f.AudioFile), Start: start, End: end,
			})
		}
	}
	return segs, nil
}

func includedFiles(all []AudioFile) []AudioFile {
	var files []AudioFile
	for _, f := range all {
		if !f.Exclude && f.Duration > 0 {
			files = append(files, f)
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].Index < files[j].Index })
	return files
}

// usableChapters returns chapters sorted by start, ignoring a lone chapter
// that just spans the whole book and any zero-length entries.
func usableChapters(chs []Chapter, total float64) []Chapter {
	var out []Chapter
	for _, c := range chs {
		if c.End-c.Start >= minSegment && c.Start < total {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	if len(out) == 1 && out[0].Start < minSegment && out[0].End >= total-minSegment {
		return nil
	}
	return out
}

// cutAcrossFiles maps a [start,end) range on the book timeline to pieces of
// individual files.
func cutAcrossFiles(spans []fileSpan, start, end float64) []Segment {
	var out []Segment
	for _, f := range spans {
		fs, fe := f.offset, f.offset+f.Duration
		s, e := math.Max(start, fs), math.Min(end, fe)
		if e-s < minSegment {
			continue
		}
		seg := Segment{Ino: f.Ino, Ext: ext(f.AudioFile), Start: s - fs, End: e - fs}
		seg.Whole = seg.Start < 0.5 && seg.End > f.Duration-0.5
		if seg.Whole {
			seg.Start, seg.End = 0, f.Duration
		}
		out = append(out, seg)
	}
	return out
}

func ext(f AudioFile) string {
	return strings.ToLower(strings.TrimPrefix(f.Metadata.Ext, "."))
}
