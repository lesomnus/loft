import { inflateRawSync } from 'node:zlib'

/**
 * What a zip holds, by name: as much of the format as reading back what an
 * export wrote takes. The sizes are the central directory's, which a writer
 * that streams (Go's `archive/zip`) leaves out of each entry's own header.
 */
export function unzip(b: Buffer): Map<string, Buffer> {
	// The directory's end is the last record: 22 bytes, then a comment.
	let end = b.length - 22
	while (end >= 0 && b.readUInt32LE(end) !== 0x06054b50) end--
	if (end < 0) throw new Error('not a zip')

	const out = new Map<string, Buffer>()
	let at = b.readUInt32LE(end + 16)
	for (let i = b.readUInt16LE(end + 10); i > 0; i--) {
		if (b.readUInt32LE(at) !== 0x02014b50) throw new Error('a zip whose directory is not one')
		const method = b.readUInt16LE(at + 10)
		const size = b.readUInt32LE(at + 20)
		const name = b.toString('utf8', at + 46, at + 46 + b.readUInt16LE(at + 28))
		const local = b.readUInt32LE(at + 42)
		const data = local + 30 + b.readUInt16LE(local + 26) + b.readUInt16LE(local + 28)
		const raw = b.subarray(data, data + size)
		out.set(name, method === 0 ? raw : inflateRawSync(raw))
		at += 46 + b.readUInt16LE(at + 28) + b.readUInt16LE(at + 30) + b.readUInt16LE(at + 32)
	}
	return out
}
