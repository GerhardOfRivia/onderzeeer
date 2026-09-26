import { copyFile } from 'node:fs/promises'

// Vite rebundles the distribution; retain its accompanying license notices.
for (const name of ['LICENSE', 'NOTICE', 'swagger-ui-bundle.js.LICENSE.txt']) {
  await copyFile(
    new URL(`../node_modules/swagger-ui-dist/${name}`, import.meta.url),
    new URL(`../../internal/webui/dist/docs/${name}`, import.meta.url),
  )
}
