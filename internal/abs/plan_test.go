package abs

import "testing"

func file(idx int, ino, ext string, dur float64) AudioFile {
	return AudioFile{Index: idx, Ino: ino, Duration: dur, Metadata: FileMetadata{Filename: ino + "." + ext, Ext: "." + ext}}
}

func TestPlanSingleFileChapters(t *testing.T) {
	it := &Item{Media: Media{
		Metadata:   Metadata{Title: "Book"},
		AudioFiles: []AudioFile{file(1, "a", "m4b", 3000)},
		Chapters:   []Chapter{{Start: 0, End: 1000, Title: "One"}, {Start: 1000, End: 3000, Title: "Two"}},
	}}
	segs, err := Plan(it)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 || segs[1].Title != "Two" || segs[1].Start != 1000 || segs[1].End != 3000 || segs[0].Whole {
		t.Fatalf("%+v", segs)
	}
}

func TestPlanFilePerChapterIsWhole(t *testing.T) {
	// Typical mp3-per-chapter book: chapters align with files, so each
	// segment is a whole file and can be streamed as-is.
	it := &Item{Media: Media{
		AudioFiles: []AudioFile{file(2, "b", "mp3", 600), file(1, "a", "mp3", 500)},
		Chapters:   []Chapter{{Start: 0, End: 500, Title: "Ch 1"}, {Start: 500, End: 1100, Title: "Ch 2"}},
	}}
	segs, _ := Plan(it)
	if len(segs) != 2 || !segs[0].Whole || segs[0].Ino != "a" || !segs[1].Whole || segs[1].Ino != "b" {
		t.Fatalf("%+v", segs)
	}
}

func TestPlanChapterSpanningFiles(t *testing.T) {
	it := &Item{Media: Media{
		AudioFiles: []AudioFile{file(1, "a", "mp3", 100), file(2, "b", "mp3", 100)},
		Chapters:   []Chapter{{Start: 0, End: 50, Title: "Intro"}, {Start: 50, End: 200, Title: "Long"}},
	}}
	segs, _ := Plan(it)
	if len(segs) != 3 || segs[1].Title != "Long (1/2)" || segs[1].Start != 50 || segs[1].End != 100 ||
		segs[2].Title != "Long (2/2)" || !segs[2].Whole {
		t.Fatalf("%+v", segs)
	}
}

func TestPlanNoChaptersSplitsLongFiles(t *testing.T) {
	it := &Item{Media: Media{
		Metadata:   Metadata{Title: "Book"},
		AudioFiles: []AudioFile{file(1, "a", "m4b", 2*3600+60)},
		Chapters:   []Chapter{{Start: 0, End: 2*3600 + 60, Title: "Whole"}}, // ignored: spans everything
	}}
	segs, _ := Plan(it)
	if len(segs) != 5 || segs[0].Title != "Book (1/5)" || segs[4].End != 2*3600+60 {
		t.Fatalf("%d %+v", len(segs), segs)
	}
}

func TestPlanExcludedAndEmpty(t *testing.T) {
	f := file(1, "a", "mp3", 100)
	f.Exclude = true
	if _, err := Plan(&Item{Media: Media{AudioFiles: []AudioFile{f}}}); err == nil {
		t.Fatal("want error for no included files")
	}
}
