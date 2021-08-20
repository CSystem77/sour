export default function start() {
  Module = {
    ...Module,
    preRun: [],
    postRun: [],
    printErr: function (text) {
      text = Array.prototype.slice.call(arguments).join(' ')
      if (0) {
        // XXX disabled for safety typeof dump == 'function') {
        dump(text + '\n') // fast, straight to the real console
      } else {
        console.error(text)
      }
    },
    setStatus: function (text) {
      //if (!Module.setStatus.last)
      //Module.setStatus.last = { time: Date.now(), text: '' }
      //if (text === Module.setStatus.text) return
      //var m = text.match(/([^(]+)\((\d+(\.\d+)?)\/(\d+)\)/)
      //var now = Date.now()
      //if (m && now - Date.now() < 30) return // if this is a progress update, skip it if too soon
      //if (m) {
      //text = m[1]
      //progressElement.value = parseInt(m[2]) * 100
      //progressElement.max = parseInt(m[4]) * 100
      //progressElement.hidden = false
      //spinnerElement.hidden = false
      //} else {
      //progressElement.value = null
      //progressElement.max = null
      //progressElement.hidden = true
      //if (!text) spinnerElement.style.display = 'none'
      //}
      //statusElement.innerHTML = text
    },
    totalDependencies: 0,
    monitorRunDependencies: function (left) {
      this.totalDependencies = Math.max(this.totalDependencies, left)
      Module.setStatus(
        left
          ? 'Preparing... (' +
              (this.totalDependencies - left) +
              '/' +
              this.totalDependencies +
              ')'
          : 'All downloads complete.'
      )
    },
    goFullScreen: function () {
      Module.requestFullScreen(true, false)
    },
    onFullScreen: function (isFullScreen) {
      if (isFullScreen) {
        BananaBread.execute('screenres ' + screen.width + ' ' + screen.height)
      } else {
        BananaBread.execute('screenres ' + 640 + ' ' + 480)
      }
    },
  }
  Module.setStatus('Downloading...')

  Module.autoexec = function () {
    Module.setStatus('')
  }
}
