/*
Copyright 2011 Paul Ruane.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package main

import (
	"log"
	"path/filepath"
	"os"
	"strings"
	"strconv"
	"github.com/hanwen/go-fuse/fuse"
)

type FuseVfs struct {
	fuse.DefaultFileSystem

	databasePath string
	mountPath    string
	state        *fuse.MountState
}

func MountVfs(databasePath string, mountPath string) (*FuseVfs, error) {
	fuseVfs := FuseVfs{}
    pathNodeFs := fuse.NewPathNodeFs(&fuseVfs, nil)
	state, _, err := fuse.MountNodeFileSystem(mountPath, pathNodeFs, nil)
	if err != nil { return nil, err }

	fuseVfs.databasePath = databasePath
	fuseVfs.mountPath = mountPath
	fuseVfs.state = state

	return &fuseVfs, nil
}

func (vfs FuseVfs) Unmount() {
	vfs.state.Unmount()
}

func (vfs FuseVfs) Loop() {
	vfs.state.Loop()
}

func (vfs FuseVfs) GetAttr(name string, context *fuse.Context) (*fuse.Attr, fuse.Status) {
	switch name {
	case "":
		fallthrough
	case "tags":
	    return vfs.getTagsAttr()
	}

	path := vfs.splitPath(name)
	switch path[0] {
	case "tags":
		return vfs.getTaggedEntryAttr(path[1:])
	}

	return nil, fuse.ENOENT
}

func (vfs FuseVfs) Unlink(name string, context *fuse.Context) fuse.Status {
    fileId, err := vfs.parseFileId(name)
    if err != nil { log.Fatalf("Could not unlink: %v", err) }

    if fileId == 0 {
        // cannot unlink tag directories
        return fuse.EPERM
    }

    path := vfs.splitPath(name)
    tagNames := path[1:len(path) - 1]

    db, err := OpenDatabase(databasePath())
    if err != nil { log.Fatal(err) }

    for _, tagName := range tagNames {
        tag, err := db.TagByName(tagName)
        if err != nil { log.Fatal(err) }
        if tag == nil { log.Fatalf("Could not retrieve tag '%v'.", tagName) }

        err = db.RemoveFileTag(fileId, tag.Id)
        if err != nil { log.Fatal(err) }
    }

    return fuse.OK
}

func (vfs FuseVfs) OpenDir(name string, context *fuse.Context) (chan fuse.DirEntry, fuse.Status) {
	switch name {
        case "": return vfs.topDirectories()
        case "tags": return vfs.tagDirectories()
	}

	path := vfs.splitPath(name)
	switch path[0] {
        case "tags": return vfs.openTaggedEntryDir(path[1:])
	}

	return nil, fuse.ENOENT
}

func (vfs FuseVfs) Readlink(name string, context *fuse.Context) (string, fuse.Status) {
	path := vfs.splitPath(name)
	switch path[0] {
        case "tags": return vfs.readTaggedEntryLink(path[1:])
	}

	return "", fuse.ENOENT
}

// implementation

func (vfs FuseVfs) splitPath(path string) []string {
	return strings.Split(path, string(filepath.Separator))
}

func (vfs FuseVfs) parseFileId(name string) (uint, error) {
	parts := strings.Split(name, ".")
	count := len(parts)

	if count == 1 { return 0, nil }

	id, err := Atoui(parts[count - 2])
	if err != nil { id, err = Atoui(parts[count - 1]) }
	if err != nil { return 0, nil }

	return id, nil
}

func (vfs FuseVfs) topDirectories() (chan fuse.DirEntry, fuse.Status) {
	channel := make(chan fuse.DirEntry, 2)
	channel <- fuse.DirEntry{Name: "tags", Mode: fuse.S_IFDIR}
	close(channel)

	return channel, fuse.OK
}

func (vfs FuseVfs) tagDirectories() (chan fuse.DirEntry, fuse.Status) {
	db, err := OpenDatabase(vfs.databasePath)
	if err != nil { log.Fatal("Could not open database: %v", err) }
	defer db.Close()

	tags, err := db.Tags()
	if err != nil { log.Fatal("Could not retrieve tags: %v", err) }

	channel := make(chan fuse.DirEntry, len(tags))
	for _, tag := range tags {
		channel <- fuse.DirEntry{Name: tag.Name, Mode: fuse.S_IFDIR}
	}
	close(channel)

	return channel, fuse.OK
}

func (vfs FuseVfs) getTagsAttr() (*fuse.Attr, fuse.Status) {
	db, err := OpenDatabase(vfs.databasePath)
	if err != nil { log.Fatalf("Could not open database: %v", err) }
	defer db.Close()

    tagCount, err := db.TagCount()
    if err != nil { log.Fatalf("Could not get tag count: %v", err) }

	return &fuse.Attr{Mode: fuse.S_IFDIR | 0755, Size: uint64(tagCount) }, fuse.OK
}

func (vfs FuseVfs) getTaggedEntryAttr(path []string) (*fuse.Attr, fuse.Status) {
	pathLength := len(path)
	name := path[pathLength - 1]

	db, err := OpenDatabase(vfs.databasePath)
	if err != nil { log.Fatalf("Could not open database: %v", err) }
	defer db.Close()

	fileId, err := vfs.parseFileId(name)
	if err != nil { return nil, fuse.ENOENT }

	if fileId == 0 {
	    // if no file ID then it is a tag directory
	    fileCount, error := db.FileCountWithTags(path)
	    if error != nil { log.Fatalf("Could not retrieve count of files with tags: %v.", path) }

		return &fuse.Attr{Mode: fuse.S_IFDIR | 0755, Size: uint64(fileCount) }, fuse.OK
	}

    file, err := db.File(fileId)
    if err != nil { log.Fatalf("Could not retrieve file #%v: %v", fileId, err) }
    if file == nil { return &fuse.Attr{Mode: fuse.S_IFREG}, fuse.ENOENT }

    fileInfo, err := os.Stat(file.Path())
    var size int64
    if err == nil {
        size = fileInfo.Size()
    } else {
        size = 0
    }

	return &fuse.Attr{Mode: fuse.S_IFLNK | 0755, Size: uint64(size)}, fuse.OK
}

func (vfs FuseVfs) openTaggedEntryDir(path []string) (chan fuse.DirEntry, fuse.Status) {
	db, err := OpenDatabase(vfs.databasePath)
	if err != nil { log.Fatalf("Could not open database: %v", err) }
	defer db.Close()

	tags := path

	furtherTags, err := db.TagsForTags(tags)
	if err != nil { log.Fatalf("Could not retrieve tags for tags: %v", err) }

	files, err := db.FilesWithTags(tags)
	if err != nil { log.Fatalf("Could not retrieve tagged files: %v", err) }

	channel := make(chan fuse.DirEntry, len(files) + len(furtherTags))
	defer close(channel)

	for _, tag := range furtherTags {
		channel <- fuse.DirEntry{Name: tag.Name, Mode: fuse.S_IFDIR | 0755}
	}

	for _, file := range files {
        linkName := vfs.getLinkName(file)
		channel <- fuse.DirEntry{Name: linkName, Mode: fuse.S_IFLNK}
	}

	return channel, fuse.OK
}

func (vfs FuseVfs) readTaggedEntryLink(path []string) (string, fuse.Status) {
	name := path[len(path)-1]

	db, err := OpenDatabase(vfs.databasePath)
	if err != nil { log.Fatalf("Could not open database: %v", err) }
	defer db.Close()

	fileId, err := vfs.parseFileId(name)
	if err != nil { log.Fatalf("Could not parse file identifier: %v", err) }
	if fileId == 0 { return "", fuse.ENOENT }

	file, err := db.File(fileId)
	if err != nil { log.Fatalf("Could not find file %v in database.", fileId) }

	return file.Path(), fuse.OK
}

func (vfs FuseVfs) getLinkName(file File) string {
    extension := filepath.Ext(file.Path())
    fileName := filepath.Base(file.Path())
    linkName := fileName[0 : len(fileName) - len(extension)]
    suffix := "." + Uitoa(file.Id) + extension

    if len(linkName) + len(suffix) > 255 {
        linkName = linkName[0 : 255 - len(suffix)]
    }

    return linkName + suffix
}

func Uitoa(ui uint) string {
    return strconv.FormatUint(uint64(ui), 10)
}

func Atoui(str string) (uint, error) {
    ui64, err := strconv.ParseUint(str, 10, 0)
    return uint(ui64), err
}