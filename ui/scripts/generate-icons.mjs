import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
import { mkdir, writeFile } from 'node:fs/promises'
import sharp from 'sharp'
import pngToIco from 'png-to-ico'

const here = dirname(fileURLToPath(import.meta.url))
const publicDir = resolve(here, '..', 'public')
const src = resolve(publicDir, 'ag0.png')

const bg = { r: 26, g: 26, b: 26, alpha: 1 }

async function renderSquarePng(size) {
  return sharp(src)
    .resize(size, size, { fit: 'contain', background: bg })
    .flatten({ background: bg })
    .png()
    .toBuffer()
}

async function writeSquarePng(size, file) {
  const buf = await renderSquarePng(size)
  const out = resolve(publicDir, file)
  await writeFile(out, buf)
  console.log(`wrote ${file} (${size}x${size})`)
}

async function writeIco(sizes, file) {
  const pngs = await Promise.all(sizes.map(renderSquarePng))
  const ico = await pngToIco(pngs)
  const out = resolve(publicDir, file)
  await writeFile(out, ico)
  console.log(`wrote ${file} (${sizes.join(',')})`)
}

async function main() {
  await mkdir(publicDir, { recursive: true })
  await writeSquarePng(16, 'favicon-16x16.png')
  await writeSquarePng(32, 'favicon-32x32.png')
  await writeSquarePng(180, 'apple-touch-icon.png')
  await writeSquarePng(192, 'icon-192.png')
  await writeSquarePng(512, 'icon-512.png')
  await writeIco([16, 32, 48], 'favicon.ico')
}

main().catch((err) => {
  console.error(err)
  process.exit(1)
})