export default function start() {
  const base = document.createElement('script')
  base.src = `${ASSET_PREFIX}/preload_base.js`
  document.body.appendChild(base)

  Module = {
    ...Module,
    setPlayerModels: function () {
      BananaBread.setPlayerModelInfo(
        'snoutx10k',
        'snoutx10k',
        'snoutx10k',
        'snoutx10k/hudguns',
        0,
        0,
        0,
        0,
        0,
        'snoutx10k',
        'snoutx10k',
        'snoutx10k',
        true
      )
    },
    tweakDetail: () => {
      BananaBread.execute('fog 10000') // disable fog
      BananaBread.execute('maxdebris 10')
    },
    loadDefaultMap: () => {
      const { innerWidth: width, innerHeight: height } = window
      Module.setCanvasSize(width, height)
      BananaBread.execute(`screenres ${width} ${height}`)
      BananaBread.execute('map complex')
    },
    locateFile: (file) => {
      if (file.endsWith('.data')) return `${ASSET_PREFIX}/${file}`
      if (file.endsWith('.wasm')) return `/game/${file}`
      return null
    },
    preRun: [],
    postRun: [],
    printErr: function (text) {
      if (
        // These two happen a lot while playing and they don't mean anything.
        text.startsWith('Cannot find preloaded audio') ||
        text.startsWith("Couldn't find file for:")
      )
        return
      console.error(text)
    },
    setStatus: function (text) {
      console.log(text)
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
        const { innerWidth: width, innerHeight: height } = window
        BananaBread.execute(`screenres ${width} ${height}`)
      }
    },
  }

  window.onerror = function (_, __, ___, ____, error) {
    console.log(error)
    return true
  }

  Module['removeRunDependency'] = null

  Module.setStatus('Downloading...')

  Module.autoexec = function () {
    Module.setStatus('')
  }
}
