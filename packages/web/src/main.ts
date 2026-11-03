import 'dou-ui/style.css'
import { createApp } from 'vue'
import App from './App.vue'
import { initToken } from './api'

initToken()
createApp(App).mount('#app')
