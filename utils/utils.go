package utils

// #include "limits.h"
import "C"

import (
	"golang.org/x/sys/unix"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	log "github.com/Sirupsen/logrus"
	"github.com/gluster/glusterd2/errors"
	"github.com/pborman/uuid"
)

const testXattr = "trusted.glusterfs.test"
const volumeIDXattr = "trusted.glusterfs.volume-id"
const gfidXattr = "trusted.gfid"

//PosixPathMax represents C's POSIX_PATH_MAX
const PosixPathMax = C._POSIX_PATH_MAX

// IsLocalAddress checks whether a given host/IP is local
func IsLocalAddress(host string) (bool, error) {
	laddrs, e := net.InterfaceAddrs()
	if e != nil {
		return false, e
	}
	var lips []net.IP
	for _, laddr := range laddrs {
		lipa := laddr.(*net.IPNet)
		lips = append(lips, lipa.IP)
	}

	rips, e := net.LookupIP(host)
	if e != nil {
		return false, e
	}
	for _, rip := range rips {
		for _, lip := range lips {
			if lip.Equal(rip) {
				return true, nil
			}
		}
	}
	return false, nil
}

// ParseHostAndBrickPath parses the host & brick path out of req.Bricks list
func ParseHostAndBrickPath(brickPath string) (string, string) {
	i := strings.LastIndex(brickPath, ":")
	if i == -1 {
		log.WithField("brick", brickPath).Error("Invalid brick path, it should be in the form of host:path")
		return "", ""
	}
	hostname := brickPath[0:i]
	path := brickPath[i+1 : len(brickPath)]

	return hostname, path
}

//ValidateBrickPathLength validates the length of the brick path
func ValidateBrickPathLength(brickPath string) int {
	//TODO : Check whether PATH_MAX is compatible across all distros
	if len(filepath.Clean(brickPath)) >= unix.PathMax {
		log.WithField("brick", brickPath).Error("brickpath is too long")
		return -1
	}
	return 0
}

//ValidateBrickSubDirLength validates the length of each sub directories under
//the brick path
func ValidateBrickSubDirLength(brickPath string) int {
	subdirs := strings.Split(brickPath, string(os.PathSeparator))
	// Iterate over the sub directories and validate that they don't breach
	//  _POSIX_PATH_MAX validation
	for _, subdir := range subdirs {
		if len(subdir) >= PosixPathMax {
			log.WithField("subdir", subdir).Error("sub directory path is too long")
			return -1
		}
	}
	return 0
}

//GetDeviceID fetches the device id of the device containing the file/directory
func GetDeviceID(f os.FileInfo) (int, error) {
	s := f.Sys()
	switch s := s.(type) {
	//TODO : Need to change syscall to unix, using unix.Stat_t fails in one
	//of the test
	case *syscall.Stat_t:
		return int(s.Dev), nil
	}
	return -1, errors.ErrDeviceIDNotFound
}

//ValidateBrickPathStats checks whether the brick directory can be created with
//certain validations like directory checks, whether directory is part of mount
//point etc
func ValidateBrickPathStats(brickPath string, host string, force bool) error {
	var created bool
	var rootStat, brickStat, parentStat os.FileInfo
	err := os.Mkdir(brickPath, os.ModeDir|os.ModePerm)
	if err != nil {
		if !os.IsExist(err) {
			log.WithFields(log.Fields{
				"host":  host,
				"brick": brickPath,
			}).Error("Failed to create brick - ", err.Error())
			return err
		}
	} else {
		created = true
	}
	brickStat, err = os.Lstat(brickPath)
	if err != nil {
		log.WithFields(log.Fields{
			"host":  host,
			"brick": brickPath,
		}).Error("Failed to stat on brick path - ", err.Error())
		return err
	}
	if !created && !brickStat.IsDir() {
		log.WithFields(log.Fields{
			"host":  host,
			"brick": brickPath,
		}).Error("brick path which is already present is not a directory")
		return errors.ErrBrickNotDirectory
	}

	rootStat, err = os.Lstat("/")
	if err != nil {
		log.Error("Failed to stat on / -", err.Error())
		return err
	}

	parentBrick := path.Dir(brickPath)
	parentStat, err = os.Lstat(parentBrick)
	if err != nil {
		log.WithFields(log.Fields{
			"host":        host,
			"brick":       brickPath,
			"parentBrick": parentBrick,
		}).Error("Failed to stat on parent of the brick path")
		return err
	}

	if !force {
		var parentDeviceID, rootDeviceID, brickDeviceID int
		var e error
		parentDeviceID, e = GetDeviceID(parentStat)
		if e != nil {
			log.WithFields(log.Fields{
				"host":  host,
				"brick": brickPath,
			}).Error("Failed to find the device id for parent of brick path")

			return err
		}
		rootDeviceID, e = GetDeviceID(rootStat)
		if e != nil {
			log.Error("Failed to find the device id of '/'")
			return err
		}
		brickDeviceID, e = GetDeviceID(brickStat)
		if e != nil {
			log.WithFields(log.Fields{
				"host":  host,
				"brick": brickPath,
			}).Error("Failed to find the device id of the brick")
			return err
		}
		if brickDeviceID != parentDeviceID {
			log.WithFields(log.Fields{
				"host":  host,
				"brick": brickPath,
			}).Error(errors.ErrBrickIsMountPoint.Error())
			return errors.ErrBrickIsMountPoint
		} else if parentDeviceID == rootDeviceID {
			log.WithFields(log.Fields{
				"host":  host,
				"brick": brickPath,
			}).Error(errors.ErrBrickUnderRootPartition.Error())
			return errors.ErrBrickUnderRootPartition
		}

	}

	return nil
}

//ValidateXattrSupport checks whether the underlying file system has extended
//attribute support and it also sets some internal xattrs to mark the brick in
//use
func ValidateXattrSupport(brickPath string, host string, uuid uuid.UUID, force bool) error {
	var err error
	err = unix.Setxattr(brickPath, "trusted.glusterfs.test", []byte("working"), 0)
	if err != nil {
		log.WithFields(log.Fields{"error": err.Error(),
			"brickPath": brickPath,
			"host":      host,
			"xattr":     testXattr}).Error("setxattr failed")
		return err
	}
	err = unix.Removexattr(brickPath, "trusted.glusterfs.test")
	if err != nil {
		log.WithFields(log.Fields{"error": err.Error(),
			"brickPath": brickPath,
			"host":      host,
			"xattr":     testXattr}).Fatal("removexattr failed")
		return err
	}
	if !force {
		if isBrickPathAlreadyInUse(brickPath) {
			log.WithFields(log.Fields{
				"brickPath": brickPath,
				"host":      host}).Error(errors.ErrBrickPathAlreadyInUse.Error())
			return errors.ErrBrickPathAlreadyInUse
		}
	}
	err = unix.Setxattr(brickPath, "trusted.glusterfs.volume-id", []byte(uuid), 0)
	if err != nil {
		log.WithFields(log.Fields{"error": err.Error(),
			"brickPath": brickPath,
			"host":      host,
			"xattr":     volumeIDXattr}).Error("setxattr failed")
		return err
	}

	return nil
}

func isBrickPathAlreadyInUse(brickPath string) bool {
	keys := []string{gfidXattr, volumeIDXattr}
	var p string
	var buf []byte
	p = brickPath
	for ; p != "/"; p = path.Dir(p) {
		for _, key := range keys {
			size, err := unix.Getxattr(p, key, buf)
			if err != nil {
				return false
			} else if size > 0 {
				return true
			} else {
				return false
			}

		}
	}
	return false
}