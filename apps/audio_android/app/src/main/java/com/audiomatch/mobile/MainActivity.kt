package com.audiomatch.mobile

import android.Manifest
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.webkit.*
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import android.net.http.SslError
class MainActivity : AppCompatActivity() {

    private val serverUrl = "https://10.213.15.12:8444/"

    private lateinit var webView: WebView
    private var microphoneRequest: PermissionRequest? = null
    private var fileCallback: ValueCallback<Array<Uri>>? = null

    // Permiso de cámara
    private val microphonePermission = registerForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { granted ->
        microphoneRequest?.let {
            if (granted) {
                it.grant(arrayOf(
                    PermissionRequest.RESOURCE_AUDIO_CAPTURE
                ))
            } else {
                it.deny()
            }
        }
        microphoneRequest = null
    }

    // Permiso de red local para versiones recientes de Android
    private val networkPermission = registerForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { granted ->
        if (granted) {
            webView.loadUrl(serverUrl)
        } else {
            Toast.makeText(
                this, "Permite el acceso a la red local",
                Toast.LENGTH_LONG
            ).show()
        }
    }

    // Selección de imágenes de la galería
    private val fileChooser = registerForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { result ->
        val uris = WebChromeClient.FileChooserParams.parseResult(
            result.resultCode, result.data
        )?.filter { it.scheme == "content" }?.toTypedArray()

        fileCallback?.onReceiveValue(uris)
        fileCallback = null
    }

    private fun trusted(uri: Uri): Boolean {
        return uri.scheme == "https" &&
                uri.host == "10.213.15.12" &&
                uri.port == 8444
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        webView = WebView(this)
        setContentView(webView)

        ViewCompat.setOnApplyWindowInsetsListener(webView) { view, insets ->
            val bars = insets.getInsets(
                WindowInsetsCompat.Type.systemBars()
            )

            view.setPadding(
                bars.left, bars.top, bars.right, bars.bottom
            )
            insets
        }

        // Configuración del frontend
        webView.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            allowFileAccess = false
            mediaPlaybackRequiresUserGesture = false
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
        }

        webView.webViewClient = object : WebViewClient() {

            override fun shouldOverrideUrlLoading(
                view: WebView,
                request: WebResourceRequest
            ): Boolean {
                return !trusted(request.url)
            }

            override fun onReceivedError(
                view: WebView,
                request: WebResourceRequest,
                error: WebResourceError
            ) {
                if (request.isForMainFrame) {
                    Toast.makeText(
                        this@MainActivity,
                        "Error: ${error.description}",
                        Toast.LENGTH_LONG
                    ).show()
                }
            }

            override fun onReceivedSslError(
                view: WebView,
                handler: SslErrorHandler,
                error: SslError
            ) {
                handler.cancel()

                Toast.makeText(
                    this@MainActivity,
                    "Error SSL: ${error.primaryError}",
                    Toast.LENGTH_LONG
                ).show()
            }

            override fun onPageFinished(
                view: WebView,
                url: String
            ) {
                Toast.makeText(
                    this@MainActivity,
                    "Carga finalizada",
                    Toast.LENGTH_SHORT
                ).show()
            }
        }

        webView.webChromeClient = object : WebChromeClient() {

            override fun onPermissionRequest(request: PermissionRequest) {

                if (!trusted(request.origin) ||
                    PermissionRequest.RESOURCE_AUDIO_CAPTURE
                    !in request.resources
                ) {
                    request.deny()
                    return
                }

                if (checkSelfPermission(Manifest.permission.RECORD_AUDIO)
                    == PackageManager.PERMISSION_GRANTED
                ) {
                    request.grant(arrayOf(
                        PermissionRequest.RESOURCE_AUDIO_CAPTURE
                    ))
                } else {
                    microphoneRequest = request
                    microphonePermission.launch(Manifest.permission.RECORD_AUDIO)
                }
            }

            override fun onPermissionRequestCanceled(
                request: PermissionRequest
            ) {
                if (microphoneRequest == request) {
                    microphoneRequest = null
                }
            }

            override fun onShowFileChooser(
                webView: WebView,
                callback: ValueCallback<Array<Uri>>,
                params: FileChooserParams
            ): Boolean {

                fileCallback?.onReceiveValue(null)
                fileCallback = callback

                try {
                    fileChooser.launch(params.createIntent())
                } catch (e: Exception) {
                    fileCallback?.onReceiveValue(null)
                    fileCallback = null
                }

                return true
            }
        }

        val permission = "android.permission.ACCESS_LOCAL_NETWORK"

        if (Build.VERSION.SDK_INT >= 37 &&
            applicationInfo.targetSdkVersion >= 37 &&
            checkSelfPermission(permission)
            != PackageManager.PERMISSION_GRANTED
        ) {
            networkPermission.launch(permission)
        } else {
            webView.loadUrl(serverUrl)
        }
    }

    override fun onDestroy() {
        fileCallback?.onReceiveValue(null)
        webView.destroy()
        super.onDestroy()
    }
}