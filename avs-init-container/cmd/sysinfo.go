package main

import "syscall"

// SysinfoFunc is a type alias for the syscall.Sysinfo function
type SysinfoFunc func(*syscall.Sysinfo_t) error

// Sysinfo is a variable that can be mocked in tests
var Sysinfo SysinfoFunc = syscall.Sysinfo
