import SwaggerUIBundle from 'swagger-ui-dist/swagger-ui-bundle.js'
import 'swagger-ui-dist/swagger-ui.css'
import './style.css'
import { swaggerOptions } from './options.js'

SwaggerUIBundle({
  ...swaggerOptions,
  dom_id: '#swagger-ui',
  presets: [SwaggerUIBundle.presets.apis],
})
