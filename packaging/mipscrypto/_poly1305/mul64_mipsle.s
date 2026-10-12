//go:build linux && mipsle && gc && !purego

#include "textflag.h"

// Four unsigned hardware products, explicit carries, no data-dependent branch.
// No floating point, calls, stack allocation or reserved R22/R23/R30 changes.
TEXT ·mul64MIPS(SB), NOSPLIT, $-4-32
	MOVW a_lo+0(FP), R1
	MOVW a_hi+4(FP), R2
	MOVW b_lo+8(FP), R3
	MOVW b_hi+12(FP), R4
	MULU R3, R1
	MOVW LO, R8
	MOVW HI, R9
	MULU R4, R1
	MOVW LO, R10
	MOVW HI, R11
	MULU R3, R2
	MOVW LO, R12
	MOVW HI, R13
	MULU R4, R2
	MOVW LO, R14
	MOVW HI, R15

	ADDU R10, R9
	SGTU R10, R9, R16
	ADDU R12, R9
	SGTU R12, R9, R17
	ADDU R17, R16
	ADDU R13, R11
	SGTU R13, R11, R18
	ADDU R14, R11
	SGTU R14, R11, R19
	ADDU R16, R11
	SGTU R16, R11, R20
	ADDU R19, R18
	ADDU R20, R18
	ADDU R18, R15

	MOVW R8, lo_lo+16(FP)
	MOVW R9, lo_hi+20(FP)
	MOVW R11, hi_lo+24(FP)
	MOVW R15, hi_hi+28(FP)
	RET
