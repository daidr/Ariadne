import 'dou-ui/style.css'
import { createApp } from 'vue'
import App from './App.vue'
import { initToken } from './api'
import { watchPresence } from './presence'

initToken()
createApp(App).mount('#app')
watchPresence()
