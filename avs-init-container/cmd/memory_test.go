package main

import (
	"os"
	"strconv"
	"syscall"
	"testing"
)

func TestCalculateMemoryConfig(t *testing.T) {
	// Save original Sysinfo function
	originalSysinfo := Sysinfo

	// Restore original Sysinfo function after test
	defer func() {
		Sysinfo = originalSysinfo
	}()

	tests := []struct {
		name             string
		totalMemory      uint64
		heapPercent      int
		heapFloorMiB     int
		heapCeilMiB      int
		directPercent    int
		metaspaceMiB     int
		systemReserveMiB int
		wantHeapMiB      int
		wantDirectMiB    int
		wantErr          bool
	}{
		{
			name:             "Normal case with 8GB total memory",
			totalMemory:      8 * 1024 * 1024 * 1024, // 8GB
			heapPercent:      65,
			heapFloorMiB:     1024,
			heapCeilMiB:      262144,
			directPercent:    10,
			metaspaceMiB:     256,
			systemReserveMiB: 2048,
			wantHeapMiB:      5325, // (8GB - 2GB) * 65% = 6GB * 65% = 3.9GB ≈ 3994MiB
			wantDirectMiB:    819,  // (8GB - 2GB) * 10% = 6GB * 10% = 614MiB
			wantErr:          false,
		},
		{
			name:             "Small memory with floor",
			totalMemory:      2 * 1024 * 1024 * 1024, // 2GB
			heapPercent:      65,
			heapFloorMiB:     1024,
			heapCeilMiB:      262144,
			directPercent:    10,
			metaspaceMiB:     256,
			systemReserveMiB: 2048,
			wantHeapMiB:      1024, // Floor of 1GB
			wantDirectMiB:    0,    // No direct memory available
			wantErr:          false,
		},
		{
			name:             "Large memory with ceiling",
			totalMemory:      512 * 1024 * 1024 * 1024, // 512GB
			heapPercent:      65,
			heapFloorMiB:     1024,
			heapCeilMiB:      262144,
			directPercent:    10,
			metaspaceMiB:     256,
			systemReserveMiB: 2048,
			wantHeapMiB:      262144, // Ceiling of 256GB
			wantDirectMiB:    524288, // (512GB - 2GB) * 10% = 510GB * 10% = 51GB
			wantErr:          false,
		},
		{
			name:             "Invalid heap percent",
			totalMemory:      8 * 1024 * 1024 * 1024,
			heapPercent:      0,
			heapFloorMiB:     1024,
			heapCeilMiB:      262144,
			directPercent:    10,
			metaspaceMiB:     256,
			systemReserveMiB: 2048,
			wantErr:          true,
		},
		{
			name:             "Invalid direct percent",
			totalMemory:      8 * 1024 * 1024 * 1024,
			heapPercent:      65,
			heapFloorMiB:     1024,
			heapCeilMiB:      262144,
			directPercent:    -1,
			metaspaceMiB:     256,
			systemReserveMiB: 2048,
			wantErr:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables for memory configuration
			os.Setenv("AEROSPIKE_VECTOR_SEARCH_HEAP_PERCENT", strconv.Itoa(tt.heapPercent))
			os.Setenv("AEROSPIKE_VECTOR_SEARCH_HEAP_FLOOR_MIB", strconv.Itoa(tt.heapFloorMiB))
			os.Setenv("AEROSPIKE_VECTOR_SEARCH_HEAP_CEIL_MIB", strconv.Itoa(tt.heapCeilMiB))
			os.Setenv("AEROSPIKE_VECTOR_SEARCH_DIRECT_PERCENT", strconv.Itoa(tt.directPercent))
			os.Setenv("AEROSPIKE_VECTOR_SEARCH_METASPACE_MIB", strconv.Itoa(tt.metaspaceMiB))
			os.Setenv("AEROSPIKE_VECTOR_SEARCH_SYSTEM_RESERVE_MIB", strconv.Itoa(tt.systemReserveMiB))

			// Mock Sysinfo function
			Sysinfo = func(info *syscall.Sysinfo_t) error {
				info.Totalram = tt.totalMemory
				return nil
			}

			heapMiB, directMiB, err := CalculateMemoryConfig()

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if heapMiB != tt.wantHeapMiB {
				t.Errorf("Heap size = %d MiB, want %d MiB", heapMiB, tt.wantHeapMiB)
			}

			if directMiB != tt.wantDirectMiB {
				t.Errorf("Direct memory = %d MiB, want %d MiB", directMiB, tt.wantDirectMiB)
			}
		})
	}
}
