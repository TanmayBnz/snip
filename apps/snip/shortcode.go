package main

import "strings"

const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

func Encode(id int64) string {
	if id == 0 {
		return string(alphabet[0])
	}
	var sb strings.Builder
	for id > 0 {
		remainder := id % 62
		sb.WriteByte(alphabet[remainder])
		id /= 62
	}
	return reverseASCII(sb.String())
}

func reverseASCII(s string) string {
	bytes := []byte(s)
	for i, j := 0, len(bytes)-1; i < j; i, j = i+1, j-1 {
		bytes[i], bytes[j] = bytes[j], bytes[i]
	}
	return string(bytes)
}
