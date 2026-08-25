package mission

const correlationWindowsFileTypeDisk uint32 = 0x0001

func correlationWindowsFileTypeAllowed(fileType uint32) bool {
	return fileType == correlationWindowsFileTypeDisk
}
