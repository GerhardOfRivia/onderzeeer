import SwaggerParser from '@apidevtools/swagger-parser'
import { fileURLToPath } from 'node:url'

const specPath = fileURLToPath(new URL('../../internal/webui/openapi.json', import.meta.url))
const document = await SwaggerParser.validate(specPath, { resolve: { external: false } })
console.log(`Validated OpenAPI ${document.openapi}: ${Object.keys(document.paths).length} paths`)
