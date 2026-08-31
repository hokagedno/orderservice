// Package id генерирует идентификаторы UUID v4 средствами стандартной
// библиотеки — отдельная зависимость ради 20 строк кода не нужна.
package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewUUID возвращает случайный UUID версии 4 в каноническом виде
// 8-4-4-4-12, например "1f0b3c8e-2a4d-4f5a-9c1e-7b2d5e6f8a90".
func NewUUID() (string, error) {
	var b [16]byte
	// crypto/rand — криптостойкий источник. math/rand здесь нельзя:
	// его последовательность предсказуема, идентификаторы стали бы угадываемыми.
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("id: не удалось получить случайные байты: %w", err)
	}

	b[6] = (b[6] & 0x0f) | 0x40 // версия 4 в старших битах 7-го байта
	b[8] = (b[8] & 0x3f) | 0x80 // вариант RFC 4122 в 9-м байте

	buf := make([]byte, 36)
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf), nil
}

// MustUUID — вариант для мест, где ошибка генератора случайных чисел
// означает неработоспособность процесса (например, инициализация).
func MustUUID() string {
	s, err := NewUUID()
	if err != nil {
		panic(err)
	}
	return s
}
