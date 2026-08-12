package main

// Linux evdev keycode -> X11 keysym.
//
// NDI KVM carries X11 keysyms, not Linux keycodes. We deliberately send the
// *unshifted* keysym and let Shift travel as its own key event, so the remote
// end applies its own layout — the same approach RFB takes. Trying to resolve
// shifted characters here would bake this machine's layout into the wire.

const (
	xkBackSpace = 0xFF08
	xkTab       = 0xFF09
	xkReturn    = 0xFF0D
	xkEscape    = 0xFF1B
	xkDelete    = 0xFFFF
	xkHome      = 0xFF50
	xkLeft      = 0xFF51
	xkUp        = 0xFF52
	xkRight     = 0xFF53
	xkDown      = 0xFF54
	xkPageUp    = 0xFF55
	xkPageDown  = 0xFF56
	xkEnd       = 0xFF57
	xkInsert    = 0xFF63
	xkMenu      = 0xFF67
	xkNumLock   = 0xFF7F
	xkF1        = 0xFFBE
	xkShiftL    = 0xFFE1
	xkShiftR    = 0xFFE2
	xkControlL  = 0xFFE3
	xkControlR  = 0xFFE4
	xkCapsLock  = 0xFFE5
	xkAltL      = 0xFFE9
	xkAltR      = 0xFFEA
	xkSuperL    = 0xFFEB
	xkSuperR    = 0xFFEC
	xkScrollLck = 0xFF14
	xkPause     = 0xFF13
	xkPrint     = 0xFF61
)

var keycodeToKeysym = map[uint16]int32{
	1: xkEscape,
	2: '1', 3: '2', 4: '3', 5: '4', 6: '5', 7: '6', 8: '7', 9: '8', 10: '9', 11: '0',
	12: '-', 13: '=', 14: xkBackSpace, 15: xkTab,
	16: 'q', 17: 'w', 18: 'e', 19: 'r', 20: 't', 21: 'y', 22: 'u', 23: 'i', 24: 'o', 25: 'p',
	26: '[', 27: ']', 28: xkReturn, 29: xkControlL,
	30: 'a', 31: 's', 32: 'd', 33: 'f', 34: 'g', 35: 'h', 36: 'j', 37: 'k', 38: 'l',
	39: ';', 40: '\'', 41: '`', 42: xkShiftL, 43: '\\',
	44: 'z', 45: 'x', 46: 'c', 47: 'v', 48: 'b', 49: 'n', 50: 'm',
	51: ',', 52: '.', 53: '/', 54: xkShiftR,
	55: '*', 56: xkAltL, 57: ' ', 58: xkCapsLock,

	59: xkF1, 60: xkF1 + 1, 61: xkF1 + 2, 62: xkF1 + 3, 63: xkF1 + 4,
	64: xkF1 + 5, 65: xkF1 + 6, 66: xkF1 + 7, 67: xkF1 + 8, 68: xkF1 + 9,
	87: xkF1 + 10, 88: xkF1 + 11,

	69: xkNumLock, 70: xkScrollLck,

	// keypad — mapped to their plain equivalents; good enough for a console
	71: xkHome, 72: xkUp, 73: xkPageUp, 74: '-',
	75: xkLeft, 76: '5', 77: xkRight, 78: '+',
	79: xkEnd, 80: xkDown, 81: xkPageDown, 82: xkInsert, 83: xkDelete,

	96: xkReturn, // KP_Enter
	97: xkControlR,
	98: '/', // KP_Divide
	99: xkPrint,
	100: xkAltR,

	102: xkHome, 103: xkUp, 104: xkPageUp, 105: xkLeft,
	106: xkRight, 107: xkEnd, 108: xkDown, 109: xkPageDown,
	110: xkInsert, 111: xkDelete,

	119: xkPause,
	125: xkSuperL, 126: xkSuperR, 127: xkMenu,
}

// Keysym returns the X11 keysym for a Linux keycode, and whether it is known.
func Keysym(code uint16) (int32, bool) {
	k, ok := keycodeToKeysym[code]
	return k, ok
}
